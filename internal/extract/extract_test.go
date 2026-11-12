package extract

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dumitruPuggle/go-assessment-asana/internal/client"
)

var fixedNow = time.Unix(1762951234, 0)

// fakeAsana serves two workspaces. User "shared" belongs to both, so it is
// returned by /users for each workspace and must be de-duplicated.
func fakeAsana(t *testing.T) *client.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws := r.URL.Query().Get("workspace")
		switch r.URL.Path {
		case "/workspaces":
			fmt.Fprint(w, `{"data":[{"gid":"w1","name":"One"},{"gid":"w2","name":"Two"}]}`)
		case "/users":
			both := `{"gid":"shared","name":"Sam","email":"sam@x.io","workspaces":[{"gid":"w1","name":"One"},{"gid":"w2","name":"Two"}]}`
			fmt.Fprintf(w, `{"data":[%s,{"gid":"u-%s","name":"Only %s","email":"%s@x.io","workspaces":[{"gid":"%s","name":"WS %s"}]}]}`,
				both, ws, ws, ws, ws, ws)
		case "/projects":
			fmt.Fprintf(w, `{"data":[{"gid":"p-%s","name":"Proj %s","archived":false,"workspace":{"gid":"%s","name":"WS %s"}}]}`, ws, ws, ws, ws)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return newClient(t, srv.URL)
}

func newClient(t *testing.T, url string) *client.Client {
	t.Helper()
	c, err := client.New("tok", client.WithBaseURL(url))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func newExtractor(t *testing.T, c *client.Client, out string) *Extractor {
	t.Helper()
	e, err := New(c, out, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.now = func() time.Time { return fixedNow }
	return e
}

func readJSON[T any](t *testing.T, path string) T {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s is not valid JSON: %v\n%s", path, err, raw)
	}
	return v
}

func TestNewValidation(t *testing.T) {
	if _, err := New(nil, "dist", nil); err == nil {
		t.Error("expected error for nil client")
	}
	c, _ := client.New("tok")
	if _, err := New(c, "", nil); err == nil {
		t.Error("expected error for empty outDir")
	}
}

func TestRunWritesProjectsSnapshot(t *testing.T) {
	out := filepath.Join(t.TempDir(), "dist")
	if err := newExtractor(t, fakeAsana(t), out).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	got := readJSON[Snapshot[client.Project]](t, filepath.Join(out, ProjectsFile))
	if got.LastUpdatedAt != fixedNow.Unix() {
		t.Errorf("lastUpdatedAt = %d, want %d", got.LastUpdatedAt, fixedNow.Unix())
	}
	if len(got.Results) != 2 || got.Results[0].GID != "p-w1" || got.Results[1].Workspace.GID != "w2" {
		t.Fatalf("results = %+v", got.Results)
	}
}

func TestRunWritesDedupedUsersSnapshot(t *testing.T) {
	out := filepath.Join(t.TempDir(), "dist")
	if err := newExtractor(t, fakeAsana(t), out).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	got := readJSON[Snapshot[client.User]](t, filepath.Join(out, UsersFile))
	if got.LastUpdatedAt != fixedNow.Unix() {
		t.Errorf("lastUpdatedAt = %d, want %d", got.LastUpdatedAt, fixedNow.Unix())
	}
	gids := []string{}
	for _, u := range got.Results {
		gids = append(gids, u.GID)
	}
	if fmt.Sprint(gids) != "[shared u-w1 u-w2]" {
		t.Fatalf("user gids = %v, want [shared u-w1 u-w2] (shared user once)", gids)
	}
	if sam := got.Results[0]; sam.Email != "sam@x.io" || len(sam.Workspaces) != 2 {
		t.Errorf("shared user = %+v", sam)
	}
}

// Pins the exact on-disk shape
// exactly two top-level keys, lastUpdatedAt a JSON number (Unix seconds).
func TestSnapshotSchema(t *testing.T) {
	out := t.TempDir()
	if err := newExtractor(t, fakeAsana(t), out).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{UsersFile, ProjectsFile} {
		raw := readJSON[map[string]any](t, filepath.Join(out, name))
		if len(raw) != 2 {
			t.Errorf("%s: top-level keys = %v, want exactly lastUpdatedAt and results", name, raw)
		}
		ts, ok := raw["lastUpdatedAt"].(float64)
		if !ok || int64(ts) != fixedNow.Unix() {
			t.Errorf("%s: lastUpdatedAt = %#v, want number %d", name, raw["lastUpdatedAt"], fixedNow.Unix())
		}
		if _, ok := raw["results"].([]any); !ok {
			t.Errorf("%s: results = %#v, want array", name, raw["results"])
		}
	}

	proj := readJSON[map[string]any](t, filepath.Join(out, ProjectsFile))["results"].([]any)[0].(map[string]any)
	for _, key := range []string{"gid", "name", "archived", "workspace"} {
		if _, ok := proj[key]; !ok {
			t.Errorf("project entry missing %q: %v", key, proj)
		}
	}
	user := readJSON[map[string]any](t, filepath.Join(out, UsersFile))["results"].([]any)[0].(map[string]any)
	for _, key := range []string{"gid", "name", "email", "workspaces"} {
		if _, ok := user[key]; !ok {
			t.Errorf("user entry missing %q: %v", key, user)
		}
	}
}

func TestEmptyWritesEmptyResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/workspaces" {
			fmt.Fprint(w, `{"data":[{"gid":"w1","name":"Empty"}]}`)
			return
		}
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer srv.Close()

	out := t.TempDir()
	if err := newExtractor(t, newClient(t, srv.URL), out).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{UsersFile, ProjectsFile} {
		raw, _ := os.ReadFile(filepath.Join(out, name))
		want := fmt.Sprintf("{\n  \"lastUpdatedAt\": %d,\n  \"results\": []\n}\n", fixedNow.Unix())
		if string(raw) != want {
			t.Errorf("%s = %q, want %q", name, raw, want)
		}
	}
}

func TestWritesNothingOnAPIError(t *testing.T) {
	// Users succeed but projects fail: neither file may be written.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/workspaces":
			fmt.Fprint(w, `{"data":[{"gid":"w1","name":"One"}]}`)
		case "/users":
			fmt.Fprint(w, `{"data":[{"gid":"u1","name":"Ann"}]}`)
		default:
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"errors":[{"message":"Forbidden"}]}`)
		}
	}))
	defer srv.Close()

	out := t.TempDir()
	if err := newExtractor(t, newClient(t, srv.URL), out).Run(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	for _, name := range []string{UsersFile, ProjectsFile} {
		if _, err := os.Stat(filepath.Join(out, name)); !os.IsNotExist(err) {
			t.Errorf("%s should not be written on API failure", name)
		}
	}
}

// A later run must refresh lastUpdatedAt, which is what a scheduled consumer
// uses to tell whether data is fresh.
func TestSecondRunUpdatesTimestamp(t *testing.T) {
	out := t.TempDir()
	e := newExtractor(t, fakeAsana(t), out)
	if err := e.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	later := fixedNow.Add(30 * time.Second)
	e.now = func() time.Time { return later }
	if err := e.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := readJSON[Snapshot[client.Project]](t, filepath.Join(out, ProjectsFile))
	if got.LastUpdatedAt != later.Unix() {
		t.Fatalf("lastUpdatedAt = %d, want %d", got.LastUpdatedAt, later.Unix())
	}
}

// pagedServer serves one workspace with nUsers users and nProjects projects in
// pages of pageSize. failProjectsPage (1-based, 0 = never) returns HTTP 500 for
// that projects page, simulating an API failure mid-stream.
func pagedServer(t *testing.T, nUsers, nProjects, pageSize, failProjectsPage int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		page := 0
		if o := q.Get("offset"); o != "" {
			fmt.Sscan(o, &page)
		}
		var total int
		var item func(i int) string
		switch r.URL.Path {
		case "/workspaces":
			fmt.Fprint(w, `{"data":[{"gid":"w1","name":"Big"}]}`)
			return
		case "/users":
			total = nUsers
			item = func(i int) string {
				return fmt.Sprintf(`{"gid":"u%d","name":"User %d","email":"u%d@x.io","workspaces":[{"gid":"w1","name":"Big"}]}`, i, i, i)
			}
		case "/projects":
			if failProjectsPage > 0 && page+1 == failProjectsPage {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			total = nProjects
			item = func(i int) string {
				return fmt.Sprintf(`{"gid":"p%d","name":"Project %d","archived":false,"workspace":{"gid":"w1","name":"Big"}}`, i, i)
			}
		}
		start, end := page*pageSize, min((page+1)*pageSize, total)
		fmt.Fprint(w, `{"data":[`)
		for i := start; i < end; i++ {
			if i > start {
				fmt.Fprint(w, ",")
			}
			fmt.Fprint(w, item(i))
		}
		fmt.Fprint(w, `]`)
		if end < total {
			fmt.Fprintf(w, `,"next_page":{"offset":"%d"}`, page+1)
		}
		fmt.Fprint(w, `}`)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// fastClient has a rate limit high enough not to slow tests down.
func fastClient(t *testing.T, url string) *client.Client {
	t.Helper()
	c, err := client.New("tok", client.WithBaseURL(url), client.WithRateLimit(1_000_000, time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// runWithDeadline fails instead of hanging if the pipeline deadlocks.
func runWithDeadline(t *testing.T, ctx context.Context, e *Extractor) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return: pipeline deadlock")
		return nil
	}
}

// seedPrevious writes a known-good previous snapshot and returns its bytes.
func seedPrevious(t *testing.T, out string) map[string]string {
	t.Helper()
	if err := newExtractor(t, fakeAsana(t), out).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	prev := map[string]string{}
	for _, name := range []string{UsersFile, ProjectsFile} {
		b, _ := os.ReadFile(filepath.Join(out, name))
		prev[name] = string(b)
	}
	return prev
}

func assertUnchanged(t *testing.T, out string, prev map[string]string) {
	t.Helper()
	for name, want := range prev {
		got, _ := os.ReadFile(filepath.Join(out, name))
		if string(got) != want {
			t.Errorf("%s was modified by a failed run", name)
		}
	}
	entries, _ := os.ReadDir(out)
	if len(entries) != len(prev) {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("leftover files in %s: %v", out, names)
	}
}

func TestStreamsManyPagesInOrder(t *testing.T) {
	const nUsers, nProjects = 2_500, 25_000
	out := t.TempDir()
	e := newExtractor(t, fastClient(t, pagedServer(t, nUsers, nProjects, 100, 0)), out)
	if err := runWithDeadline(t, context.Background(), e); err != nil {
		t.Fatal(err)
	}

	projects := readJSON[Snapshot[client.Project]](t, filepath.Join(out, ProjectsFile))
	if len(projects.Results) != nProjects {
		t.Fatalf("projects = %d, want %d", len(projects.Results), nProjects)
	}
	for i, p := range projects.Results {
		if p.GID != fmt.Sprintf("p%d", i) {
			t.Fatalf("results[%d] = %s: order not preserved across pages", i, p.GID)
		}
	}
	users := readJSON[Snapshot[client.User]](t, filepath.Join(out, UsersFile))
	if len(users.Results) != nUsers || users.LastUpdatedAt != fixedNow.Unix() {
		t.Fatalf("users = %d (ts %d), want %d (ts %d)", len(users.Results), users.LastUpdatedAt, nUsers, fixedNow.Unix())
	}
}

func TestAPIFailureMidStreamKeepsPreviousFiles(t *testing.T) {
	out := t.TempDir()
	prev := seedPrevious(t, out)

	// Projects page 7 of 10 fails after 600 projects were already streamed.
	e := newExtractor(t, fastClient(t, pagedServer(t, 50, 1_000, 100, 7)), out)
	if err := runWithDeadline(t, context.Background(), e); err == nil {
		t.Fatal("expected error")
	}
	assertUnchanged(t, out, prev)
}

// failingWriter wraps the real writer and fails on the nth Write. If before
// is set, it runs just before failing (used to line up a race on purpose).
type failingWriter struct {
	snapshotWriter
	failAt, n int
	before    func()
}

var errDiskFull = errors.New("disk full")

func (f *failingWriter) Write(v any) error {
	f.n++
	if f.n == f.failAt {
		if f.before != nil {
			f.before()
		}
		return errDiskFull
	}
	return f.snapshotWriter.Write(v)
}

// A writer that fails mid-stream must stop the fetcher (no deadlock even
// though the fetcher has far more pages to send than the channel holds), and
// the error reported must be the disk error, not the cancellation.
func TestWriterFailureMidStreamNoDeadlock(t *testing.T) {
	out := t.TempDir()
	prev := seedPrevious(t, out)

	var requests atomic.Int32
	url := pagedServer(t, 10, 50_000, 100, 0)
	counting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		resp, err := http.Get(url + r.URL.RequestURI())
		if err != nil {
			t.Error(err)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer counting.Close()

	e := newExtractor(t, fastClient(t, counting.URL), out)
	real := e.newWriter
	e.newWriter = func(path string, ts int64) (snapshotWriter, error) {
		w, err := real(path, ts)
		if err != nil || filepath.Base(path) != ProjectsFile {
			return w, err
		}
		// Fail on the very first project, but only once the fetcher has
		// filled the channel and is blocked handing over the next page: that
		// is exactly the state in which a naive pipeline deadlocks.
		// Requests: workspaces + users page + projects page 1 (being written)
		// + pageBuffer queued pages + 1 page the fetcher is stuck sending.
		blocked := int32(2 + 1 + pageBuffer + 1)
		return &failingWriter{snapshotWriter: w, failAt: 1, before: func() {
			deadline := time.Now().Add(5 * time.Second)
			for requests.Load() < blocked && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			time.Sleep(50 * time.Millisecond) // let it reach the blocking send
		}}, nil
	}

	err := runWithDeadline(t, context.Background(), e)
	if !errors.Is(err, errDiskFull) {
		t.Fatalf("err = %v, want the writer's disk error", err)
	}
	assertUnchanged(t, out, prev)
	// 500 projects pages exist; the fetcher must stop right after the failure
	// instead of crawling on: at most the pages already queued plus one.
	if n := requests.Load(); n > int32(2+1+pageBuffer+2) {
		t.Fatalf("fetcher made %d requests after writer failed; it was not stopped", n)
	}
}

func TestCancelledMidRunKeepsPreviousFiles(t *testing.T) {
	out := t.TempDir()
	prev := seedPrevious(t, out)

	ctx, cancel := context.WithCancel(context.Background())
	e := newExtractor(t, fastClient(t, pagedServer(t, 10, 50_000, 100, 0)), out)
	real := e.newWriter
	e.newWriter = func(path string, ts int64) (snapshotWriter, error) {
		w, err := real(path, ts)
		if err != nil {
			return w, err
		}
		return &cancelAfter{snapshotWriter: w, at: 1_000, cancel: cancel}, nil
	}

	if err := runWithDeadline(t, ctx, e); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	assertUnchanged(t, out, prev)
}

// cancelAfter cancels the run's parent context after `at` writes, simulating
// Ctrl-C in the middle of a large extraction.
type cancelAfter struct {
	snapshotWriter
	at, n  int
	cancel context.CancelFunc
}

func (c *cancelAfter) Write(v any) error {
	c.n++
	if c.n == c.at {
		c.cancel()
	}
	return c.snapshotWriter.Write(v)
}
