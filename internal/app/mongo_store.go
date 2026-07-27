package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

const mongoStateID = "tempo"

type mongoStateDocument struct {
	ID            string    `bson:"_id"`
	SchemaVersion int       `bson:"schemaVersion"`
	UpdatedAt     time.Time `bson:"updatedAt"`
	State         State     `bson:"state"`
}

type mongoStateBackend struct {
	client     *mongo.Client
	collection *mongo.Collection
	timeout    time.Duration
}

func NewMongoStore(ctx context.Context, uri, database, collection, importPath string) (*Store, error) {
	if uri == "" {
		return nil, errors.New("MongoDB URI is required")
	}
	if database == "" {
		database = "tempo"
	}
	if collection == "" {
		collection = "app_state"
	}
	client, err := mongo.Connect(options.Client().ApplyURI(uri).SetAppName("tempo"))
	if err != nil {
		return nil, fmt.Errorf("connect MongoDB: %w", err)
	}
	backend := &mongoStateBackend{client: client, collection: client.Database(database).Collection(collection), timeout: 5 * time.Second}
	if err = backend.Check(ctx); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("ping MongoDB: %w", err)
	}
	var initial *State
	if importPath != "" {
		state, loadErr := loadStateFile(importPath)
		if loadErr == nil {
			initial = &state
		} else if !errors.Is(loadErr, os.ErrNotExist) {
			_ = client.Disconnect(context.Background())
			return nil, fmt.Errorf("load MongoDB import source: %w", loadErr)
		}
	}
	store, err := newStore(backend, initial)
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
			return State{}, os.ErrNotExist
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

func (b *mongoStateBackend) Check(ctx context.Context) error {
	checkCtx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	return b.client.Ping(checkCtx, readpref.Primary())
}

func (b *mongoStateBackend) Close(ctx context.Context) error { return b.client.Disconnect(ctx) }
func (b *mongoStateBackend) Name() string                    { return "mongodb" }
