package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
