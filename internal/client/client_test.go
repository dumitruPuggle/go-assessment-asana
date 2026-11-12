package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

const testToken = "test-token"

func newTestClient(t *testing.T, h http.HandlerFunc, opts ...Option) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(testToken, append([]Option{WithBaseURL(srv.URL + "/api/1.0")}, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestNew(t *testing.T) {
	t.Run("rejects empty token", func(t *testing.T) {
		if _, err := New("  "); err == nil {
			t.Fatal("expected error for empty token")
		}
	})
	t.Run("defaults to Asana base url", func(t *testing.T) {
		c, err := New(testToken)
		if err != nil {
			t.Fatal(err)
		}
		if got := c.baseURL.String(); got != DefaultBaseURL {
			t.Fatalf("base url = %q, want %q", got, DefaultBaseURL)
		}
	})
	t.Run("rejects relative base url", func(t *testing.T) {
		if _, err := New(testToken, WithBaseURL("/api")); err == nil {
			t.Fatal("expected error for relative base url")
		}
	})
	t.Run("rejects bad page size", func(t *testing.T) {
		if _, err := New(testToken, WithPageSize(0)); err == nil {
			t.Fatal("expected error for page size 0")
		}
	})
}

func TestRequestHeadersAndPath(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/api/1.0/workspaces" {
			t.Errorf("path = %s, want /api/1.0/workspaces", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		fmt.Fprint(w, `{"data":[{"gid":"1","name":"Acme"}]}`)
	})

	ws, err := c.ListWorkspaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 1 || ws[0] != (Workspace{GID: "1", Name: "Acme"}) {
		t.Fatalf("workspaces = %+v", ws)
	}
}

func TestListUsersPaginates(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		q := r.URL.Query()
		if q.Get("workspace") != "ws1" || q.Get("limit") != "2" || q.Get("opt_fields") != "name,email,workspaces.name" {
			t.Errorf("unexpected query: %s", r.URL.RawQuery)
		}
		switch q.Get("offset") {
		case "":
			fmt.Fprint(w, `{"data":[{"gid":"1","name":"Ann","email":"ann@x.io"},{"gid":"2","name":"Bob","email":"bob@x.io"}],
				"next_page":{"offset":"abc","path":"/users?offset=abc","uri":"..."}}`)
		case "abc":
			fmt.Fprint(w, `{"data":[{"gid":"3","name":"Cy","email":"cy@x.io"}],"next_page":null}`)
		default:
			t.Errorf("unexpected offset %q", q.Get("offset"))
		}
	}, WithPageSize(2))

	users, err := c.ListUsers(context.Background(), "ws1")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	want := []User{{GID: "1", Name: "Ann", Email: "ann@x.io"}, {GID: "2", Name: "Bob", Email: "bob@x.io"}, {GID: "3", Name: "Cy", Email: "cy@x.io"}}
	if len(users) != len(want) {
		t.Fatalf("got %d users, want %d", len(users), len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(users[i], want[i]) {
			t.Errorf("users[%d] = %+v, want %+v", i, users[i], want[i])
		}
	}
}

func TestListProjects(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/1.0/projects" || r.URL.Query().Get("workspace") != "ws1" {
			t.Errorf("unexpected request %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		fmt.Fprint(w, `{"data":[{"gid":"p1","name":"Launch","archived":true}]}`)
	})

	projects, err := c.ListProjects(context.Background(), "ws1")
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0] != (Project{GID: "p1", Name: "Launch", Archived: true}) {
		t.Fatalf("projects = %+v", projects)
	}
}

// Mirrors the GET /projects pagination contract: next_page.offset is echoed
// back as ?offset=, other params are preserved, and next_page:null ends it.
func TestListProjectsPaginates(t *testing.T) {
	var offsets []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("workspace") != "ws1" || q.Get("limit") != "1" || q.Get("opt_fields") != "name,archived,workspace.name" {
			t.Errorf("query params lost across pages: %s", r.URL.RawQuery)
		}
		offsets = append(offsets, q.Get("offset"))
		switch q.Get("offset") {
		case "":
			fmt.Fprint(w, `{"data":[{"gid":"p1","name":"A"}],"next_page":{"offset":"tok1","path":"/projects?limit=1&offset=tok1","uri":"https://app.asana.com/api/1.0/projects?limit=1&offset=tok1"}}`)
		case "tok1":
			fmt.Fprint(w, `{"data":[{"gid":"p2","name":"B"}],"next_page":{"offset":"tok2","path":"/projects?limit=1&offset=tok2","uri":"https://app.asana.com/api/1.0/projects?limit=1&offset=tok2"}}`)
		case "tok2":
			fmt.Fprint(w, `{"data":[{"gid":"p3","name":"C"}],"next_page":null}`)
		}
	}, WithPageSize(1))

	projects, err := c.ListProjects(context.Background(), "ws1")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(offsets) != "[ tok1 tok2]" {
		t.Errorf("offsets sent = %q, want [\"\" tok1 tok2]", offsets)
	}
	if len(projects) != 3 || projects[0].GID != "p1" || projects[2].GID != "p3" {
		t.Fatalf("projects = %+v", projects)
	}
}

func TestPaginationRepeatedOffset(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"data":[{"gid":"1"}],"next_page":{"offset":"same"}}`)
	})
	if _, err := c.ListWorkspaces(context.Background()); err == nil {
		t.Fatal("expected error on repeated offset")
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestWorkspaceRequired(t *testing.T) {
	c, _ := New(testToken)
	if _, err := c.ListUsers(context.Background(), ""); err == nil {
		t.Error("ListUsers: expected error for empty workspace")
	}
	if _, err := c.ListProjects(context.Background(), ""); err == nil {
		t.Error("ListProjects: expected error for empty workspace")
	}
}

func TestAPIError(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		retryAfter string
		wantMsg    string
		wantRetry  time.Duration
	}{
		{
			name:    "unauthorized with asana error body",
			status:  http.StatusUnauthorized,
			body:    `{"errors":[{"message":"Not Authorized"}]}`,
			wantMsg: "asana api error (401): Not Authorized",
		},
		{
			name:       "rate limited",
			status:     http.StatusTooManyRequests,
			body:       `{"errors":[{"message":"Rate limit exceeded"}]}`,
			retryAfter: "30",
			wantMsg:    "asana api error (429): Rate limit exceeded",
			wantRetry:  30 * time.Second,
		},
		{
			name:    "non-json body falls back to status text",
			status:  http.StatusBadGateway,
			body:    `<html>oops</html>`,
			wantMsg: "asana api error (502): Bad Gateway",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if tt.retryAfter != "" {
					w.Header().Set("Retry-After", tt.retryAfter)
				}
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			})

			_, err := c.ListWorkspaces(context.Background())
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("err = %v, want *APIError", err)
			}
			if apiErr.StatusCode != tt.status {
				t.Errorf("status = %d, want %d", apiErr.StatusCode, tt.status)
			}
			if apiErr.Error() != tt.wantMsg {
				t.Errorf("message = %q, want %q", apiErr.Error(), tt.wantMsg)
			}
			if apiErr.RetryAfter != tt.wantRetry {
				t.Errorf("retry after = %v, want %v", apiErr.RetryAfter, tt.wantRetry)
			}
		})
	}
}

func TestMalformedJSON(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":`)
	})
	if _, err := c.ListWorkspaces(context.Background()); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestContextCanceled(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[]}`)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.ListWorkspaces(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestEachProjectsPageDeliversPagesAsTheyArrive(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("offset") {
		case "":
			fmt.Fprint(w, `{"data":[{"gid":"p1"},{"gid":"p2"}],"next_page":{"offset":"o1"}}`)
		case "o1":
			fmt.Fprint(w, `{"data":[{"gid":"p3"}]}`)
		}
	}, WithPageSize(2))

	var sizes []int
	err := c.EachProjectsPage(context.Background(), "ws1", func(page []Project) error {
		sizes = append(sizes, len(page))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(sizes) != "[2 1]" {
		t.Fatalf("page sizes = %v, want [2 1]", sizes)
	}
}

func TestEachUsersPageStopsWhenCallbackFails(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprintf(w, `{"data":[{"gid":"u%d"}],"next_page":{"offset":"o%d"}}`, calls, calls)
	})

	stop := errors.New("stop")
	err := c.EachUsersPage(context.Background(), "ws1", func([]User) error { return stop })
	if !errors.Is(err, stop) {
		t.Fatalf("err = %v, want callback error", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (no further pages after callback error)", calls)
	}
}

func TestEachPageRequiresWorkspace(t *testing.T) {
	c, _ := New(testToken)
	noop := func([]User) error { return nil }
	if err := c.EachUsersPage(context.Background(), "", noop); err == nil {
		t.Error("EachUsersPage: expected error for empty workspace")
	}
	if err := c.EachProjectsPage(context.Background(), "", func([]Project) error { return nil }); err == nil {
		t.Error("EachProjectsPage: expected error for empty workspace")
	}
}
