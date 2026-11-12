package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/dumitruPuggle/go-assessment-asana/internal/client"
)

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

	c, err := client.New("tok", client.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	return c
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

func TestExportWritesProjects(t *testing.T) {
	out := filepath.Join(t.TempDir(), "dist")
	if err := export(context.Background(), fakeAsana(t), out); err != nil {
		t.Fatal(err)
	}

	got := readJSON[[]client.Project](t, filepath.Join(out, "projects.json"))
	if len(got) != 2 || got[0].GID != "p-w1" || got[1].Workspace.GID != "w2" {
		t.Fatalf("projects = %+v", got)
	}
}

func TestExportWritesDedupedUsers(t *testing.T) {
	out := filepath.Join(t.TempDir(), "dist")
	if err := export(context.Background(), fakeAsana(t), out); err != nil {
		t.Fatal(err)
	}

	got := readJSON[[]client.User](t, filepath.Join(out, "users.json"))
	gids := []string{}
	for _, u := range got {
		gids = append(gids, u.GID)
	}
	if fmt.Sprint(gids) != "[shared u-w1 u-w2]" {
		t.Fatalf("user gids = %v, want [shared u-w1 u-w2] (shared user once)", gids)
	}

	sam := got[0]
	if sam.Name != "Sam" || sam.Email != "sam@x.io" || len(sam.Workspaces) != 2 {
		t.Errorf("shared user = %+v, want name, email and both workspaces", sam)
	}
}

// Pins the on-disk shape of users.json so field renames are caught.
func TestUsersJSONSchema(t *testing.T) {
	out := t.TempDir()
	if err := export(context.Background(), fakeAsana(t), out); err != nil {
		t.Fatal(err)
	}

	raw := readJSON[[]map[string]any](t, filepath.Join(out, "users.json"))
	for _, key := range []string{"gid", "name", "email", "workspaces"} {
		if _, ok := raw[0][key]; !ok {
			t.Errorf("users.json entry missing %q: %v", key, raw[0])
		}
	}
	ws := raw[0]["workspaces"].([]any)[0].(map[string]any)
	if ws["gid"] != "w1" || ws["name"] != "One" {
		t.Errorf("workspace entry = %v", ws)
	}
}

func TestExportEmptyWritesEmptyArrays(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/workspaces" {
			fmt.Fprint(w, `{"data":[{"gid":"w1","name":"Empty"}]}`)
			return
		}
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer srv.Close()

	c, _ := client.New("tok", client.WithBaseURL(srv.URL))
	out := t.TempDir()
	if err := export(context.Background(), c, out); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"users.json", "projects.json"} {
		raw, _ := os.ReadFile(filepath.Join(out, name))
		if string(raw) != "[]\n" {
			t.Errorf("%s = %q, want []", name, raw)
		}
	}
}

func TestExportWritesNothingOnAPIError(t *testing.T) {
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

	c, _ := client.New("tok", client.WithBaseURL(srv.URL))
	out := t.TempDir()
	if err := export(context.Background(), c, out); err == nil {
		t.Fatal("expected error")
	}
	for _, name := range []string{"users.json", "projects.json"} {
		if _, err := os.Stat(filepath.Join(out, name)); !os.IsNotExist(err) {
			t.Errorf("%s should not be written on API failure", name)
		}
	}
}
