package storage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

type workspace struct {
	GID  string `json:"gid"`
	Name string `json:"name"`
}

type project struct {
	GID       string    `json:"gid"`
	Name      string    `json:"name"`
	Archived  bool      `json:"archived"`
	Workspace workspace `json:"workspace"`
}

type document struct {
	LastUpdatedAt int64     `json:"lastUpdatedAt"`
	Results       []project `json:"results"`
}

const ts = 1762951234

func sample(i int) project {
	return project{
		GID:       fmt.Sprintf("12204723334%05d", i),
		Name:      fmt.Sprintf("Project %d \"quoted\" ünïcødé", i),
		Archived:  i%3 == 0,
		Workspace: workspace{GID: "1220472063194862", Name: "My workspace"},
	}
}

func writeAll(t *testing.T, path string, items []project) {
	t.Helper()
	w, err := NewSnapshotWriter(path, ts)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Abort()
	for _, it := range items {
		if err := w.Write(it); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(); err != nil {
		t.Fatal(err)
	}
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// The streamed output must be exactly what encoding the whole document at
// once would produce, for 0, 1 and many records.
func TestOutputMatchesMarshalIndent(t *testing.T) {
	for _, n := range []int{0, 1, 3} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			items := []project{}
			for i := 0; i < n; i++ {
				items = append(items, sample(i))
			}
			path := filepath.Join(t.TempDir(), "dist", "projects.json")
			writeAll(t, path, items)

			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want, _ := json.MarshalIndent(document{ts, items}, "", "  ")
			want = append(want, '\n')
			if string(got) != string(want) {
				t.Fatalf("streamed output differs from MarshalIndent\n got: %s\nwant: %s", got, want)
			}
			if info, _ := os.Stat(path); info.Mode().Perm() != 0o644 {
				t.Errorf("mode = %v, want 0644", info.Mode().Perm())
			}
		})
	}
}

func TestCommitReplacesAndLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.json")
	writeAll(t, path, []project{sample(1)})
	writeAll(t, path, []project{sample(2), sample(3)})

	var doc document
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &doc); err != nil || len(doc.Results) != 2 {
		t.Fatalf("expected replaced content with 2 results, got %s", raw)
	}
	if names := dirEntries(t, dir); len(names) != 1 {
		t.Fatalf("dir = %v, want only projects.json", names)
	}
}

// A run that fails halfway (here: aborted after 500 records) must leave the
// previous file byte-for-byte intact and clean up its temp file.
func TestAbortMidStreamKeepsPreviousFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.json")
	writeAll(t, path, []project{sample(1)})
	before, _ := os.ReadFile(path)

	w, err := NewSnapshotWriter(path, ts+60)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 500; i++ {
		if err := w.Write(sample(i)); err != nil {
			t.Fatal(err)
		}
	}
	w.Abort()

	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("previous file was modified by an aborted write")
	}
	if names := dirEntries(t, dir); len(names) != 1 {
		t.Fatalf("temp file leaked: %v", names)
	}
}

func TestEncodeErrorAbortsAndIsSticky(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.json")
	writeAll(t, path, []project{sample(1)})
	before, _ := os.ReadFile(path)

	w, _ := NewSnapshotWriter(path, ts)
	_ = w.Write(sample(2))
	if err := w.Write(make(chan int)); err == nil { // channels can't be JSON-encoded
		t.Fatal("expected encode error")
	}
	if err := w.Write(sample(3)); err == nil {
		t.Error("Write after failure must fail")
	}
	if err := w.Commit(); err == nil {
		t.Error("Commit after failure must fail")
	}

	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("previous file was modified after an encode error")
	}
	if names := dirEntries(t, dir); len(names) != 1 {
		t.Fatalf("temp file leaked: %v", names)
	}
}

func TestLifecycleMisuse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projects.json")
	w, _ := NewSnapshotWriter(path, ts)
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	if err := w.Write(sample(1)); err == nil {
		t.Error("Write after Finish must fail (document already closed)")
	}
	if err := w.Commit(); err != nil {
		t.Fatalf("Commit after Finish: %v", err)
	}
	if err := w.Commit(); err == nil {
		t.Error("second Commit must fail")
	}
	w.Abort() // after Commit: must be a no-op...
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("...and must not delete the committed file: %v", err)
	}
}

func TestFinishBeforeCommitDoesNotPublish(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projects.json")
	w, _ := NewSnapshotWriter(path, ts)
	defer w.Abort()
	_ = w.Write(sample(1))
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("Finish must not publish the file; only Commit does")
	}
}

// One million records: memory must stay flat (it would be hundreds of MB if
// records or the encoded document were held in memory), and the result must
// be valid JSON that can itself be read back without loading it all.
func TestMillionRecordsConstantMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 1M-record test in -short mode")
	}
	const n = 1_000_000
	path := filepath.Join(t.TempDir(), "projects.json")

	w, err := NewSnapshotWriter(path, ts)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Abort()

	var ms runtime.MemStats
	var peakHeap uint64
	for i := 0; i < n; i++ {
		if err := w.Write(sample(i)); err != nil {
			t.Fatal(err)
		}
		if i%100_000 == 0 {
			runtime.GC()
			runtime.ReadMemStats(&ms)
			peakHeap = max(peakHeap, ms.HeapAlloc)
		}
	}
	if err := w.Commit(); err != nil {
		t.Fatal(err)
	}

	info, _ := os.Stat(path)
	const heapBudget = 32 << 20 // 32 MB, far below the file size
	if peakHeap > heapBudget {
		t.Fatalf("peak live heap %d MB while writing a %d MB file; memory is not constant",
			peakHeap>>20, info.Size()>>20)
	}

	// Read it back as a stream too, counting records.
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	dec := json.NewDecoder(bufio.NewReader(f))
	count := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if tok == "results" {
			if _, err := dec.Token(); err != nil { // '['
				t.Fatal(err)
			}
			for dec.More() {
				var p project
				if err := dec.Decode(&p); err != nil {
					t.Fatalf("record %d: %v", count, err)
				}
				count++
			}
		}
	}
	if count != n {
		t.Fatalf("read back %d records, want %d", count, n)
	}
	t.Logf("wrote %d records, %d MB on disk, peak live heap %.1f MB",
		n, info.Size()>>20, float64(peakHeap)/(1<<20))
}
