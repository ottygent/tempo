package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type memoryBackend struct {
	state    State
	loadErr  error
	saves    int
	checkErr error
}

func (b *memoryBackend) Load() (State, error)        { return b.state, b.loadErr }
func (b *memoryBackend) Save(state State) error      { b.state, b.saves = state, b.saves+1; return nil }
func (b *memoryBackend) Check(context.Context) error { return b.checkErr }
func (b *memoryBackend) Close(context.Context) error { return nil }
func (b *memoryBackend) Name() string                { return "memory" }

func TestStoreImportsInitialStateOnlyWhenBackendIsEmpty(t *testing.T) {
	initial := seedState()
	initial.Workspaces[0].Name = "Imported workspace"
	backend := &memoryBackend{loadErr: os.ErrNotExist}
	store, err := newStore(backend, &initial)
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
	store, err = newStore(backend, &initial)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Snapshot().Workspaces[0].Name; got != "MongoDB wins" {
		t.Fatalf("workspace = %q", got)
	}
	if backend.saves != 0 {
		t.Fatalf("unexpected overwrite: %d saves", backend.saves)
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

func TestMongoStoreRoundTrip(t *testing.T) {
	uri := os.Getenv("TEMPO_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("TEMPO_TEST_MONGO_URI is not set")
	}
	path := filepath.Join(t.TempDir(), "import.json")
	fileStore, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	initial := fileStore.Snapshot()
	collection := "app_state_integration_test"
	store, err := NewMongoStore(context.Background(), uri, "tempo", collection, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		backend := store.backend.(*mongoStateBackend)
		_ = backend.collection.Drop(context.Background())
		_ = store.Close(context.Background())
	})
	if len(store.Snapshot().Tasks) != len(initial.Tasks) {
		t.Fatal("JSON import did not preserve tasks")
	}
	created, err := store.AddWorkspace(Workspace{Name: "Mongo round trip"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	store, err = NewMongoStore(context.Background(), uri, "tempo", collection, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, workspace := range store.Snapshot().Workspaces {
		found = found || workspace.ID == created.ID
	}
	if !found {
		t.Fatal("MongoDB state did not survive reconnect")
	}
}
