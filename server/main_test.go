package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateLegacyDB(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"karaver.db", "karaver.db-wal"} {
		os.WriteFile(filepath.Join(dir, f), []byte(f), 0o644)
	}
	newPath := filepath.Join(dir, "zkaraver.db")
	if err := migrateLegacyDB(dir, newPath); err != nil {
		t.Fatal(err)
	}
	for f, want := range map[string]string{"zkaraver.db": "karaver.db", "zkaraver.db-wal": "karaver.db-wal"} {
		if b, err := os.ReadFile(filepath.Join(dir, f)); err != nil || string(b) != want {
			t.Fatalf("%s = %q, %v", f, b, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "karaver.db")); !os.IsNotExist(err) {
		t.Fatal("old database still there")
	}

	// An existing new database is never overwritten.
	os.WriteFile(filepath.Join(dir, "karaver.db"), []byte("stale"), 0o644)
	if err := migrateLegacyDB(dir, newPath); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(newPath); string(b) != "karaver.db" {
		t.Fatalf("new database overwritten: %q", b)
	}
}
