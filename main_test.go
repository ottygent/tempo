package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadAdminPasswordFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bootstrap-password")
	if err := os.WriteFile(path, []byte("correct-horse-battery-staple\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	password, err := readAdminPasswordFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if password != "correct-horse-battery-staple" {
		t.Fatal("bootstrap password was not read correctly")
	}
}

func TestReadAdminPasswordFileRejectsUnsafeInputs(t *testing.T) {
	dir := t.TempDir()
	unsafe := filepath.Join(dir, "unsafe-password")
	if err := os.WriteFile(unsafe, []byte("correct-horse-battery-staple"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readAdminPasswordFile(unsafe); err == nil {
		t.Fatal("group-readable bootstrap password file was accepted")
	}

	target := filepath.Join(dir, "target-password")
	if err := os.WriteFile(target, []byte("correct-horse-battery-staple"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "password-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readAdminPasswordFile(link); err == nil {
		t.Fatal("symlinked bootstrap password file was accepted")
	}
}
