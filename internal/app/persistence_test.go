package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
)

type memoryBackend struct {
	state    State
	loadErr  error
	saves    int
	checkErr error
}

func (b *memoryBackend) Load() (State, error) { return b.state, b.loadErr }
func (b *memoryBackend) Save(state State) error {
	b.state, b.loadErr, b.saves = state, nil, b.saves+1
	return nil
}
func (b *memoryBackend) Check(context.Context) error { return b.checkErr }
func (b *memoryBackend) Close(context.Context) error { return nil }
func (b *memoryBackend) Name() string                { return "memory" }

type memoryAuthRepository struct {
	mu            sync.Mutex
	record        *authRecord
	initializeErr error
	updateErr     error
	updates       int
}

func (r *memoryAuthRepository) LoadAuth() (authRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.record == nil {
		return authRecord{}, errAuthNotFound
	}
	return cloneAuthRecord(*r.record), nil
}

func (r *memoryAuthRepository) InitializeAuth(credentials authCredentials) (authRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.initializeErr != nil {
		return authRecord{}, r.initializeErr
	}
	if r.record == nil {
		record := authRecord{Credentials: cloneAuthCredentials(credentials), Revision: 1}
		r.record = &record
	}
	return cloneAuthRecord(*r.record), nil
}

func (r *memoryAuthRepository) UpdateAuth(expectedRevision int64, next authCredentials) (authRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updateErr != nil {
		return authRecord{}, r.updateErr
	}
	if r.record == nil || r.record.Revision != expectedRevision {
		return authRecord{}, errAuthConflict
	}
	record := authRecord{Credentials: cloneAuthCredentials(next), Revision: expectedRevision + 1}
	r.record = &record
	r.updates++
	return cloneAuthRecord(record), nil
}

func (r *memoryAuthRepository) Name() string { return "memory" }

func (r *memoryAuthRepository) snapshot() authRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.record == nil {
		return authRecord{}
	}
	return cloneAuthRecord(*r.record)
}

func cloneAuthRecord(record authRecord) authRecord {
	record.Credentials = cloneAuthCredentials(record.Credentials)
	return record
}

func newMemoryStore(t *testing.T) (*Store, *memoryBackend) {
	t.Helper()
	backend := &memoryBackend{loadErr: errStateNotFound}
	store, err := newStore(backend, nil)
	if err != nil {
		t.Fatal(err)
	}
	return store, backend
}

func newMemoryAuth(t *testing.T, repository *memoryAuthRepository, username, password string, secure bool) *Auth {
	t.Helper()
	auth, err := newAuth(repository, "", username, password, secure)
	if err != nil {
		t.Fatal(err)
	}
	return auth
}

func TestStoreImportsInitialStateOnlyWhenBackendIsEmpty(t *testing.T) {
	initial := seedState()
	initial.Workspaces[0].Name = "Imported workspace"
	backend := &memoryBackend{loadErr: errStateNotFound}
	store, err := newStore(backend, func() (State, error) { return initial, nil })
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Snapshot().Workspaces[0].Name; got != "Imported workspace" {
		t.Fatalf("workspace = %q", got)
	}
	if backend.saves != 1 {
		t.Fatalf("initial saves = %d", backend.saves)
	}

	existing := seedState()
	existing.Workspaces[0].Name = "MongoDB wins"
	backend = &memoryBackend{state: existing}
	loaderCalled := false
	store, err = newStore(backend, func() (State, error) {
		loaderCalled = true
		return initial, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Snapshot().Workspaces[0].Name; got != "MongoDB wins" {
		t.Fatalf("workspace = %q", got)
	}
	if backend.saves != 0 || loaderCalled {
		t.Fatalf("legacy state was consulted despite existing Mongo state: saves=%d loaderCalled=%v", backend.saves, loaderCalled)
	}
}

func TestStoreHealthUsesPersistenceBackend(t *testing.T) {
	backend := &memoryBackend{state: seedState(), checkErr: errors.New("offline")}
	store, err := newStore(backend, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(store.Check(context.Background()), backend.checkErr) {
		t.Fatal("health error was not propagated")
	}
}

func TestMongoStoreRejectsSharedStateAndAuthCollection(t *testing.T) {
	_, err := NewMongoStore(context.Background(), "mongodb://127.0.0.1:27017", "tempo", "shared", "shared", "")
	if err == nil || !strings.Contains(err.Error(), "must be different") {
		t.Fatalf("shared MongoDB collection was not rejected: %v", err)
	}
}

func TestMongoStateAndAuthRoundTrip(t *testing.T) {
	uri := os.Getenv("TEMPO_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("TEMPO_TEST_MONGO_URI is not set")
	}
	suffix := newID("test")
	stateCollection := "app_state_integration_" + suffix
	authCollection := "auth_integration_" + suffix
	store, err := NewMongoStore(context.Background(), uri, "tempo", stateCollection, authCollection, "")
	if err != nil {
		t.Fatal(err)
	}
	cleanup := func(current *Store) {
		backend := current.backend.(*mongoStateBackend)
		_ = backend.collection.Drop(context.Background())
		_ = backend.authCollection.Drop(context.Background())
		_ = current.Close(context.Background())
	}
	defer func() { cleanup(store) }()

	auth, err := NewAuthForStore(store, "", "admin", testPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.AddWorkspace(Workspace{Name: "Mongo round trip"})
	if err != nil {
		t.Fatal(err)
	}
	if !auth.CheckCredentials("admin", testPassword) {
		t.Fatal("fresh MongoDB credentials were rejected")
	}
	if err = store.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	store, err = NewMongoStore(context.Background(), uri, "tempo", stateCollection, authCollection, "")
	if err != nil {
		t.Fatal(err)
	}
	auth, err = NewAuthForStore(store, "", "ignored", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !auth.CheckCredentials("admin", testPassword) {
		t.Fatal("MongoDB credentials did not survive reconnect")
	}
	found := false
	for _, workspace := range store.Snapshot().Workspaces {
		found = found || workspace.ID == created.ID
	}
	if !found {
		t.Fatal("MongoDB state did not survive reconnect")
	}
}
