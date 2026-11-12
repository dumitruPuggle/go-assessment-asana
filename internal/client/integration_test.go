//go:build integration

// Live tests against the real Asana API. Run with:
//
//	go test -tags integration ./internal/client/ -v
//
// The token is read from ASANA_PERSONAL_ACCESS_TOKEN or the repo-root .env.
package client

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/dumitruPuggle/go-assessment-asana/internal/config"
)

func liveClient(t *testing.T, opts ...Option) *Client {
	t.Helper()
	cfg, err := config.Load("../../.env")
	if err != nil {
		t.Skipf("no Asana token available: %v", err)
	}
	c, err := New(cfg.AsanaToken, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func liveCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func firstWorkspace(t *testing.T, c *Client) Workspace {
	t.Helper()
	ws, err := c.ListWorkspaces(liveCtx(t))
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(ws) == 0 {
		t.Skip("token has no workspaces")
	}
	return ws[0]
}

func TestLiveListWorkspaces(t *testing.T) {
	c := liveClient(t)
	ws, err := c.ListWorkspaces(liveCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) == 0 {
		t.Fatal("expected at least one workspace")
	}
	for _, w := range ws {
		if w.GID == "" || w.Name == "" {
			t.Errorf("incomplete workspace: %+v", w)
		}
	}
	t.Logf("%d workspace(s)", len(ws))
}

func TestLiveListUsers(t *testing.T) {
	c := liveClient(t)
	ws := firstWorkspace(t, c)
	users, err := c.ListUsers(liveCtx(t), ws.GID)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) == 0 {
		t.Fatal("expected at least one user (the token owner)")
	}
	for _, u := range users {
		if u.GID == "" || u.Name == "" {
			t.Errorf("incomplete user: gid=%q name=%q", u.GID, u.Name)
		}
		if u.Email == "" {
			t.Errorf("user %s has no email (opt_fields not applied?)", u.GID)
		}
		inWS := false
		for _, w := range u.Workspaces {
			inWS = inWS || w.GID == ws.GID
		}
		if !inWS {
			t.Errorf("user %s workspaces %+v do not include %s", u.GID, u.Workspaces, ws.GID)
		}
	}
	t.Logf("%d user(s) in first workspace", len(users))
}

func TestLiveListProjects(t *testing.T) {
	c := liveClient(t)
	ws := firstWorkspace(t, c)
	projects, err := c.ListProjects(liveCtx(t), ws.GID)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range projects {
		if p.GID == "" || p.Name == "" {
			t.Errorf("incomplete project: %+v", p)
		}
		if p.Workspace.GID != ws.GID {
			t.Errorf("project %s workspace = %q, want %q", p.GID, p.Workspace.GID, ws.GID)
		}
	}
	t.Logf("%d project(s) in first workspace", len(projects))
}

// Page size 1 forces real offset pagination whenever there are >=2 projects;
// results must match a single large-page fetch with no duplicates.
func TestLivePaginationMatchesSinglePage(t *testing.T) {
	big := liveClient(t)
	ws := firstWorkspace(t, big)
	all, err := big.ListProjects(liveCtx(t), ws.GID)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < 2 {
		t.Skipf("need >=2 projects to exercise pagination, have %d", len(all))
	}
	if len(all) > 20 {
		all = nil // avoid burning quota on a huge page-size-1 crawl
		t.Skip("workspace too large for page-size-1 crawl")
	}

	small := liveClient(t, WithPageSize(1))
	paged, err := small.ListProjects(liveCtx(t), ws.GID)
	if err != nil {
		t.Fatal(err)
	}
	if len(paged) != len(all) {
		t.Fatalf("paged = %d projects, single page = %d", len(paged), len(all))
	}
	seen := map[string]bool{}
	for _, p := range paged {
		if seen[p.GID] {
			t.Fatalf("duplicate project %s across pages", p.GID)
		}
		seen[p.GID] = true
	}
	t.Logf("paged through %d projects one at a time", len(paged))
}

func TestLiveInvalidTokenIsUnauthorized(t *testing.T) {
	c, err := New("0/invalid-token-for-integration-test")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ListWorkspaces(liveCtx(t))
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("err = %v, want 401 APIError", err)
	}
}
