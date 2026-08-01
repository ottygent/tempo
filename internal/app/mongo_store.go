package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"
)

const (
	mongoStateID      = "tempo"
	mongoAuthID       = "tempo"
	authSchemaVersion = 1
)

type mongoStateDocument struct {
	ID            string    `bson:"_id"`
	SchemaVersion int       `bson:"schemaVersion"`
	UpdatedAt     time.Time `bson:"updatedAt"`
	State         State     `bson:"state"`
}

type mongoAuthDocument struct {
	ID                string    `bson:"_id"`
	SchemaVersion     int       `bson:"schemaVersion"`
	Revision          int64     `bson:"revision"`
	UpdatedAt         time.Time `bson:"updatedAt"`
	Username          string    `bson:"username"`
	Email             string    `bson:"email,omitempty"`
	PasswordAlgorithm string    `bson:"passwordAlgorithm"`
	Salt              []byte    `bson:"salt"`
	PasswordHash      []byte    `bson:"passwordHash"`
	PBKDFIterations   int       `bson:"pbkdfIterations,omitempty"`
	Argon2Time        uint32    `bson:"argon2Time,omitempty"`
	Argon2Memory      uint32    `bson:"argon2Memory,omitempty"`
	Argon2Threads     uint8     `bson:"argon2Threads,omitempty"`
	SessionSecret     []byte    `bson:"sessionSecret"`
}

type mongoStateBackend struct {
	client         *mongo.Client
	collection     *mongo.Collection
	authCollection *mongo.Collection
	timeout        time.Duration
}

func NewMongoStore(ctx context.Context, uri, database, stateCollection, authCollection, legacyStatePath string) (*Store, error) {
	if uri == "" {
		return nil, errors.New("TEMPO_MONGO_URI is required")
	}
	if database == "" {
		database = "tempo"
	}
	if stateCollection == "" {
		stateCollection = "app_state"
	}
	if authCollection == "" {
		authCollection = "auth"
	}
	if stateCollection == authCollection {
		return nil, errors.New("MongoDB state and authentication collections must be different")
	}
	client, err := mongo.Connect(options.Client().ApplyURI(uri).SetAppName("tempo"))
	if err != nil {
		return nil, fmt.Errorf("connect MongoDB: %w", err)
	}
	db := client.Database(database)
	backend := &mongoStateBackend{
		client:         client,
		collection:     db.Collection(stateCollection),
		authCollection: db.Collection(authCollection, options.Collection().SetReadPreference(readpref.Primary()).SetWriteConcern(writeconcern.Majority())),
		timeout:        5 * time.Second,
	}
	if err = backend.Check(ctx); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("ping MongoDB: %w", err)
	}
	var loadInitial initialStateLoader
	if legacyStatePath != "" {
		loadInitial = func() (State, error) {
			state, loadErr := loadLegacyStateFile(legacyStatePath)
			if loadErr != nil {
				return State{}, fmt.Errorf("load legacy state import: %w", loadErr)
			}
			return state, nil
		}
	}
	store, err := newStore(backend, loadInitial)
	if err != nil {
		_ = client.Disconnect(context.Background())
		return nil, err
	}
	return store, nil
}

func (b *mongoStateBackend) Load() (State, error) {
	ctx, cancel := context.WithTimeout(context.Background(), b.timeout)
	defer cancel()
	var document mongoStateDocument
	if err := b.collection.FindOne(ctx, map[string]string{"_id": mongoStateID}).Decode(&document); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return State{}, errStateNotFound
		}
		return State{}, err
	}
	return document.State, nil
}

func (b *mongoStateBackend) Save(state State) error {
	ctx, cancel := context.WithTimeout(context.Background(), b.timeout)
	defer cancel()
	document := mongoStateDocument{ID: mongoStateID, SchemaVersion: 1, UpdatedAt: time.Now().UTC(), State: state}
	_, err := b.collection.ReplaceOne(ctx, map[string]string{"_id": mongoStateID}, document, options.Replace().SetUpsert(true))
	return err
}

func (b *mongoStateBackend) LoadAuth() (authRecord, error) {
	ctx, cancel := context.WithTimeout(context.Background(), b.timeout)
	defer cancel()
	var document mongoAuthDocument
	if err := b.authCollection.FindOne(ctx, map[string]string{"_id": mongoAuthID}).Decode(&document); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return authRecord{}, errAuthNotFound
		}
		return authRecord{}, err
	}
	if document.SchemaVersion != authSchemaVersion {
		return authRecord{}, fmt.Errorf("unsupported auth schema version %d", document.SchemaVersion)
	}
	return authRecordFromDocument(document), nil
}

func (b *mongoStateBackend) InitializeAuth(credentials authCredentials) (authRecord, error) {
	document := mongoAuthDocumentFromCredentials(credentials, 1)
	ctx, cancel := context.WithTimeout(context.Background(), b.timeout)
	_, insertErr := b.authCollection.InsertOne(ctx, document)
	cancel()
	if insertErr == nil {
		return b.LoadAuth()
	}

	// InsertOne can report an ambiguous network result. A primary read-back
	// distinguishes a committed write from a real failure without overwriting
	// a credential created by another process.
	loaded, loadErr := b.LoadAuth()
	if loadErr == nil && (mongo.IsDuplicateKeyError(insertErr) || sameAuthCredentials(loaded.Credentials, credentials)) {
		return loaded, nil
	}
	return authRecord{}, fmt.Errorf("initialize MongoDB auth: %w", insertErr)
}

func (b *mongoStateBackend) UpdateAuth(expectedRevision int64, next authCredentials) (authRecord, error) {
	nextRevision := expectedRevision + 1
	document := mongoAuthDocumentFromCredentials(next, nextRevision)
	ctx, cancel := context.WithTimeout(context.Background(), b.timeout)
	result, updateErr := b.authCollection.ReplaceOne(ctx, map[string]any{"_id": mongoAuthID, "revision": expectedRevision}, document)
	cancel()
	if updateErr == nil && result.MatchedCount == 1 {
		loaded, err := b.LoadAuth()
		if err != nil {
			return authRecord{}, fmt.Errorf("verify MongoDB auth update: %w", err)
		}
		if loaded.Revision != nextRevision || !sameAuthCredentials(loaded.Credentials, next) {
			return authRecord{}, errors.New("verify MongoDB auth update: read-back mismatch")
		}
		return loaded, nil
	}

	// Handle an acknowledged-but-response-lost write by checking the primary.
	loaded, loadErr := b.LoadAuth()
	if loadErr == nil && loaded.Revision == nextRevision && sameAuthCredentials(loaded.Credentials, next) {
		return loaded, nil
	}
	if updateErr != nil {
		return authRecord{}, fmt.Errorf("update MongoDB auth: %w", updateErr)
	}
	return authRecord{}, errAuthConflict
}

func mongoAuthDocumentFromCredentials(credentials authCredentials, revision int64) mongoAuthDocument {
	return mongoAuthDocument{
		ID:                mongoAuthID,
		SchemaVersion:     authSchemaVersion,
		Revision:          revision,
		UpdatedAt:         time.Now().UTC(),
		Username:          credentials.Username,
		Email:             credentials.Email,
		PasswordAlgorithm: credentials.Password.Algorithm,
		Salt:              append([]byte(nil), credentials.Password.Salt...),
		PasswordHash:      append([]byte(nil), credentials.Password.Hash...),
		PBKDFIterations:   credentials.Password.PBKDFIterations,
		Argon2Time:        credentials.Password.Argon2Time,
		Argon2Memory:      credentials.Password.Argon2Memory,
		Argon2Threads:     credentials.Password.Argon2Threads,
		SessionSecret:     append([]byte(nil), credentials.SessionSecret...),
	}
}

func authRecordFromDocument(document mongoAuthDocument) authRecord {
	return authRecord{
		Revision: document.Revision,
		Credentials: authCredentials{
			Username: document.Username,
			Email:    document.Email,
			Password: passwordDigest{
				Algorithm:       document.PasswordAlgorithm,
				Salt:            append([]byte(nil), document.Salt...),
				Hash:            append([]byte(nil), document.PasswordHash...),
				PBKDFIterations: document.PBKDFIterations,
				Argon2Time:      document.Argon2Time,
				Argon2Memory:    document.Argon2Memory,
				Argon2Threads:   document.Argon2Threads,
			},
			SessionSecret: append([]byte(nil), document.SessionSecret...),
		},
	}
}

func (b *mongoStateBackend) Check(ctx context.Context) error {
	checkCtx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	return b.client.Ping(checkCtx, readpref.Primary())
}

func (b *mongoStateBackend) Close(ctx context.Context) error { return b.client.Disconnect(ctx) }
func (b *mongoStateBackend) Name() string                    { return "mongodb" }
