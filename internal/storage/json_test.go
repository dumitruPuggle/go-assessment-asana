package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type item struct {
	GID  string `json:"gid"`
	Name string `json:"name"`
}

func TestWriteJSONCreatesDirAndFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dist", "nested", "projects.json")
	want := []item{{"1", "A"}, {"2", "B"}}

	if err := WriteJSON(path, want); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got []item
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, raw)
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", info.Mode().Perm())
	}
}

func TestWriteJSONOverwritesAndLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.json")
	if err := WriteJSON(path, []item{{"old", "Old"}}); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(path, []item{{"new", "New"}}); err != nil {
		t.Fatal(err)
	}

	var got []item
	raw, _ := os.ReadFile(path)
	_ = json.Unmarshal(raw, &got)
	if len(got) != 1 || got[0].GID != "new" {
		t.Fatalf("got %+v, want overwritten content", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("dir has %d entries, want only projects.json", len(entries))
	}
}

func TestWriteJSONKeepsOldFileOnEncodeError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.json")
	if err := WriteJSON(path, []item{{"old", "Old"}}); err != nil {
		t.Fatal(err)
	}
	// Channels can't be JSON-encoded, forcing a failure mid-write.
	if err := WriteJSON(path, map[string]any{"bad": make(chan int)}); err == nil {
		t.Fatal("expected encode error")
	}

	var got []item
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &got); err != nil || len(got) != 1 || got[0].GID != "old" {
		t.Fatalf("previous file was not preserved: %s", raw)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp file leaked: %d entries", len(entries))
	}
}
