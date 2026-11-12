// Package storage persists fetched Asana data to local files.
package storage

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// bufferSize batches small per-record writes into large syscalls.
const bufferSize = 64 << 10

// errClosed is returned when a SnapshotWriter is used after Commit or Abort.
var errClosed = errors.New("snapshot writer is closed")

// SnapshotWriter streams records into a JSON file of the form
//
//	{
//	  "lastUpdatedAt": 1762951234,
//	  "results": [
//	    {...},
//	    {...}
//	  ]
//	}
//
// one record at a time, so memory stays constant regardless of record count.
// Output is byte-identical to json.MarshalIndent of the whole document with a
// two-space indent.
//
// Writes go to a temp file in the target directory, published atomically by
// Commit (fsync + rename). Readers of path always see either the previous
// complete file or the new complete file, never a partial one. Abort, or any
// error, discards the temp file and leaves the previous file untouched.
//
// A SnapshotWriter is not safe for concurrent use; use one per goroutine.
type SnapshotWriter struct {
	path     string
	tmp      *os.File
	buf      *bufio.Writer
	count    int
	finished bool
	closed   bool
	err      error // sticky: once set, every later call fails with it
}

// NewSnapshotWriter creates the temp file for path (creating parent
// directories as needed) and writes the header with lastUpdatedAt.
func NewSnapshotWriter(path string, lastUpdatedAt int64) (*SnapshotWriter, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return nil, fmt.Errorf("create temp file for %s: %w", path, err)
	}
	w := &SnapshotWriter{path: path, tmp: tmp, buf: bufio.NewWriterSize(tmp, bufferSize)}
	if _, err := fmt.Fprintf(w.buf, "{\n  \"lastUpdatedAt\": %d,\n  \"results\": [", lastUpdatedAt); err != nil {
		w.Abort()
		return nil, fmt.Errorf("write header to %s: %w", path, err)
	}
	return w, nil
}

// Write appends one record to results.
func (w *SnapshotWriter) Write(v any) error {
	if err := w.usable(); err != nil {
		return err
	}
	if w.finished {
		return errClosed // the document is already closed with "]}"
	}
	b, err := json.MarshalIndent(v, "    ", "  ")
	if err != nil {
		return w.fail(fmt.Errorf("encode record %d for %s: %w", w.count, w.path, err))
	}
	sep := ",\n    "
	if w.count == 0 {
		sep = "\n    "
	}
	if _, err := w.buf.WriteString(sep); err != nil {
		return w.fail(fmt.Errorf("write %s: %w", w.path, err))
	}
	if _, err := w.buf.Write(b); err != nil {
		return w.fail(fmt.Errorf("write %s: %w", w.path, err))
	}
	w.count++
	return nil
}

// Count reports how many records have been written.
func (w *SnapshotWriter) Count() int { return w.count }

// Finish closes the JSON document and makes the temp file durable (flush +
// fsync + close) without publishing it. Split from Commit so several files
// can all be finished first and then published together, minimizing the
// window in which one is fresh and another stale. Calling it again is a no-op.
func (w *SnapshotWriter) Finish() error {
	if w.finished {
		return w.err
	}
	if err := w.usable(); err != nil {
		return err
	}
	footer := "]\n}\n"
	if w.count > 0 {
		footer = "\n  ]\n}\n"
	}
	if _, err := w.buf.WriteString(footer); err != nil {
		return w.fail(fmt.Errorf("write %s: %w", w.path, err))
	}
	if err := w.buf.Flush(); err != nil {
		return w.fail(fmt.Errorf("flush %s: %w", w.path, err))
	}
	if err := w.tmp.Sync(); err != nil {
		return w.fail(fmt.Errorf("sync %s: %w", w.path, err))
	}
	if err := w.tmp.Close(); err != nil {
		return w.fail(fmt.Errorf("close %s: %w", w.path, err))
	}
	if err := os.Chmod(w.tmp.Name(), 0o644); err != nil {
		return w.fail(fmt.Errorf("chmod %s: %w", w.path, err))
	}
	w.finished = true
	return nil
}

// Commit finishes the file if needed and atomically renames it over path.
func (w *SnapshotWriter) Commit() error {
	if err := w.Finish(); err != nil {
		return err
	}
	if w.closed {
		return errClosed
	}
	if err := os.Rename(w.tmp.Name(), w.path); err != nil {
		return w.fail(fmt.Errorf("rename to %s: %w", w.path, err))
	}
	w.closed = true
	return nil
}

// Abort discards the temp file. It is safe to call at any time and more than
// once, including after Commit (where it does nothing), so callers can simply
// `defer w.Abort()`.
func (w *SnapshotWriter) Abort() {
	if w.closed {
		return
	}
	w.closed = true
	if w.err == nil {
		w.err = errClosed
	}
	w.tmp.Close() // may already be closed by Finish; error irrelevant
	os.Remove(w.tmp.Name())
}

func (w *SnapshotWriter) usable() error {
	if w.err != nil {
		return w.err
	}
	if w.closed {
		return errClosed
	}
	return nil
}

// fail records err and discards the temp file.
func (w *SnapshotWriter) fail(err error) error {
	w.err = err
	w.Abort()
	return err
}
