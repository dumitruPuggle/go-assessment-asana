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

// ListUsers returns all users in the given workspace.
func (c *Client) ListUsers(ctx context.Context, workspaceGID string) ([]User, error) {
	if workspaceGID == "" {
		return nil, errors.New("workspace gid is required")
	}
	q := url.Values{}
	q.Set("workspace", workspaceGID)
	q.Set("opt_fields", "name,email,workspaces.name")
	return listAll[User](ctx, c, "/users", q)
}

// ListProjects returns all projects in the given workspace.
func (c *Client) ListProjects(ctx context.Context, workspaceGID string) ([]Project, error) {
	if workspaceGID == "" {
		return nil, errors.New("workspace gid is required")
	}
	q := url.Values{}
	q.Set("workspace", workspaceGID)
	q.Set("opt_fields", "name,archived,workspace.name")
	return listAll[Project](ctx, c, "/projects", q)
}
