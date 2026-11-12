package client

import (
	"context"
	"errors"
	"net/url"
)

// Workspace is an Asana workspace or organization.
type Workspace struct {
	GID  string `json:"gid"`
	Name string `json:"name"`
}

// User is an Asana user.
type User struct {
	GID        string      `json:"gid"`
	Name       string      `json:"name"`
	Email      string      `json:"email"`
	Workspaces []Workspace `json:"workspaces,omitempty"`
}

// Project is an Asana project.
type Project struct {
	GID       string    `json:"gid"`
	Name      string    `json:"name"`
	Archived  bool      `json:"archived"`
	Workspace Workspace `json:"workspace,omitzero"`
}

// ListWorkspaces returns every workspace visible to the token.
func (c *Client) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	return listAll[Workspace](ctx, c, "/workspaces", nil)
}

var errNoWorkspace = errors.New("workspace gid is required")

func usersQuery(workspaceGID string) url.Values {
	q := url.Values{}
	q.Set("workspace", workspaceGID)
	q.Set("opt_fields", "name,email,workspaces.name")
	return q
}

func projectsQuery(workspaceGID string) url.Values {
	q := url.Values{}
	q.Set("workspace", workspaceGID)
	q.Set("opt_fields", "name,archived,workspace.name")
	return q
}

// ListUsers returns all users in the given workspace.
func (c *Client) ListUsers(ctx context.Context, workspaceGID string) ([]User, error) {
	if workspaceGID == "" {
		return nil, errNoWorkspace
	}
	return listAll[User](ctx, c, "/users", usersQuery(workspaceGID))
}

// EachUsersPage streams the users of a workspace one page at a time, so
// memory stays constant however many users there are. Returning an error
// from fn stops pagination.
func (c *Client) EachUsersPage(ctx context.Context, workspaceGID string, fn func([]User) error) error {
	if workspaceGID == "" {
		return errNoWorkspace
	}
	return eachPage(ctx, c, "/users", usersQuery(workspaceGID), fn)
}

// ListProjects returns all projects in the given workspace.
func (c *Client) ListProjects(ctx context.Context, workspaceGID string) ([]Project, error) {
	if workspaceGID == "" {
		return nil, errNoWorkspace
	}
	return listAll[Project](ctx, c, "/projects", projectsQuery(workspaceGID))
}

// EachProjectsPage streams the projects of a workspace one page at a time.
// Returning an error from fn stops pagination.
func (c *Client) EachProjectsPage(ctx context.Context, workspaceGID string, fn func([]Project) error) error {
	if workspaceGID == "" {
		return errNoWorkspace
	}
	return eachPage(ctx, c, "/projects", projectsQuery(workspaceGID), fn)
}
