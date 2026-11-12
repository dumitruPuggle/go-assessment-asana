// Package extract pulls users and projects from Asana and saves them as JSON
// snapshots.
package extract

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/dumitruPuggle/go-assessment-asana/internal/client"
	"github.com/dumitruPuggle/go-assessment-asana/internal/storage"
)

// File names written into the output directory.
const (
	UsersFile    = "users.json"
	ProjectsFile = "projects.json"
)

// Snapshot is the on-disk format of users.json and projects.json:
//
//	{"lastUpdatedAt": 1762951234, "results": [...]}
//
// LastUpdatedAt is the Unix time in seconds at which the data was fetched.
type Snapshot[T any] struct {
	LastUpdatedAt int64 `json:"lastUpdatedAt"`
	Results       []T   `json:"results"`
}

// Extractor fetches every workspace's users and projects and writes them to
// OutDir. Reuse one Extractor (and so one client) across scheduled runs so
// the client's rate limiter applies to all of them together.
type Extractor struct {
	client *client.Client
	outDir string
	logger *slog.Logger
	now    func() time.Time
}

// New builds an Extractor. logger may be nil.
func New(c *client.Client, outDir string, logger *slog.Logger) (*Extractor, error) {
	if c == nil {
		return nil, errors.New("extract: client must not be nil")
	}
	if outDir == "" {
		return nil, errors.New("extract: output directory must not be empty")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Extractor{client: c, outDir: outDir, logger: logger, now: time.Now}, nil
}

// Run performs one extraction. Everything is fetched before anything is
// written, so an API failure never leaves a mix of fresh and stale files.
func (e *Extractor) Run(ctx context.Context) error {
	workspaces, err := e.client.ListWorkspaces(ctx)
	if err != nil {
		return fmt.Errorf("list workspaces: %w", err)
	}

	// Non-nil so empty results encode as [] rather than null.
	users := []client.User{}
	projects := []client.Project{}
	seenUsers := map[string]bool{}

	for _, ws := range workspaces {
		us, err := e.client.ListUsers(ctx, ws.GID)
		if err != nil {
			return fmt.Errorf("list users in workspace %s: %w", ws.GID, err)
		}
		// A user in several workspaces is returned once per workspace;
		// keep one entry (its Workspaces field already lists them all).
		for _, u := range us {
			if !seenUsers[u.GID] {
				seenUsers[u.GID] = true
				users = append(users, u)
			}
		}

		ps, err := e.client.ListProjects(ctx, ws.GID)
		if err != nil {
			return fmt.Errorf("list projects in workspace %s: %w", ws.GID, err)
		}
		projects = append(projects, ps...)

		e.logger.Debug("fetched workspace", "workspace", ws.Name, "users", len(us), "projects", len(ps))
	}

	// One timestamp for both files: they describe the same moment.
	updatedAt := e.now().Unix()

	if err := e.write(UsersFile, Snapshot[client.User]{updatedAt, users}, len(users)); err != nil {
		return err
	}
	return e.write(ProjectsFile, Snapshot[client.Project]{updatedAt, projects}, len(projects))
}

func (e *Extractor) write(name string, snap any, count int) error {
	path := filepath.Join(e.outDir, name)
	if err := storage.WriteJSON(path, snap); err != nil {
		return err
	}
	e.logger.Info("saved", "file", path, "records", count)
	return nil
}
