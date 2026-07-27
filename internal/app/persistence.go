package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type stateBackend interface {
	Load() (State, error)
	Save(State) error
	Check(context.Context) error
	Close(context.Context) error
	Name() string
}

type fileStateBackend struct{ path string }

func newFileStateBackend(path string) *fileStateBackend { return &fileStateBackend{path: path} }

func (b *fileStateBackend) Load() (State, error) { return loadStateFile(b.path) }

func (b *fileStateBackend) Save(state State) error {
	if err := os.MkdirAll(filepath.Dir(b.path), 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := b.path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(append(body, '\n')); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	if err = os.Rename(tmp, b.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	dir, err := os.Open(filepath.Dir(b.path))
	if err == nil {
		err = dir.Sync()
		_ = dir.Close()
	}
	return err
}

func (b *fileStateBackend) Check(context.Context) error { return nil }
func (b *fileStateBackend) Close(context.Context) error { return nil }
func (b *fileStateBackend) Name() string                { return "json" }

func loadStateFile(path string) (State, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(body, &state); err != nil {
		return State{}, fmt.Errorf("decode state: %w", err)
	}
	return state, nil
}
