// Package extract pulls users and projects from Asana and saves them as JSON
// snapshots.
package extract

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/dumitruPuggle/go-assessment-asana/internal/client"
	"github.com/dumitruPuggle/go-assessment-asana/internal/storage"
)

// File names written into the output directory.
const (
	UsersFile    = "users.json"
	ProjectsFile = "projects.json"
)

// pageBuffer is how many fetched pages may queue for a writer. It bounds
// memory (pageBuffer x 100 records per stream) and provides backpressure: if
// the disk is slow, fetching pauses instead of buffering everything.
const pageBuffer = 4

// Snapshot is the on-disk format of users.json and projects.json:
//
//	{"lastUpdatedAt": 1762951234, "results": [...]}
//
// LastUpdatedAt is the Unix time in seconds at which the run started fetching,
// so the data is at least that fresh.
type Snapshot[T any] struct {
	LastUpdatedAt int64 `json:"lastUpdatedAt"`
	Results       []T   `json:"results"`
}

// snapshotWriter is the subset of *storage.SnapshotWriter used here, so tests
// can inject failures.
type snapshotWriter interface {
	Write(v any) error
	Count() int
	Finish() error
	Commit() error
	Abort()
}

// Extractor fetches every workspace's users and projects and streams them to
// OutDir. Reuse one Extractor (and so one client) across scheduled runs so
// the client's rate limiter applies to all of them together.
type Extractor struct {
	client *client.Client
	outDir string
	logger *slog.Logger
	now    func() time.Time

	newWriter func(path string, lastUpdatedAt int64) (snapshotWriter, error)
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
	return &Extractor{
		client: c,
		outDir: outDir,
		logger: logger,
		now:    time.Now,
		newWriter: func(path string, ts int64) (snapshotWriter, error) {
			return storage.NewSnapshotWriter(path, ts)
		},
	}, nil
}

// Run performs one extraction as a pipeline:
//
//	fetcher ──[]User pages────▶ users writer    ──▶ users.json.tmp
//	        ──[]Project pages─▶ projects writer ──▶ projects.json.tmp
//
// Fetching and disk writes overlap, each stream holds at most pageBuffer
// pages, and records are encoded one at a time, so memory is constant in the
// number of records. Both temp files are finished (fsynced) before either is
// published, and on any failure both are discarded, leaving the previous
// users.json and projects.json untouched.
func (e *Extractor) Run(ctx context.Context) error {
	updatedAt := e.now().Unix()

	usersW, err := e.newWriter(filepath.Join(e.outDir, UsersFile), updatedAt)
	if err != nil {
		return err
	}
	defer usersW.Abort() // no-op after a successful Commit
	projectsW, err := e.newWriter(filepath.Join(e.outDir, ProjectsFile), updatedAt)
	if err != nil {
		return err
	}
	defer projectsW.Abort()

	// A writer failure cancels ctx, which unblocks the fetcher if it is
	// waiting to hand over a page, so the pipeline can never deadlock.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	usersCh := make(chan []client.User, pageBuffer)
	projectsCh := make(chan []client.Project, pageBuffer)

	var wg sync.WaitGroup
	var usersErr, projectsErr error
	wg.Go(func() { usersErr = drain(usersCh, usersW, cancel) })
	wg.Go(func() { projectsErr = drain(projectsCh, projectsW, cancel) })

	fetchErr := e.fetch(ctx, usersCh, projectsCh) // closes both channels
	wg.Wait()

	// Report the root cause: a writer error, not the cancellation it caused.
	if err := errors.Join(usersErr, projectsErr); err != nil {
		return err
	}
	if fetchErr != nil {
		return fetchErr
	}

	// Make both files durable first, then publish both back to back.
	for _, w := range []snapshotWriter{usersW, projectsW} {
		if err := w.Finish(); err != nil {
			return err
		}
	}
	for _, w := range []snapshotWriter{usersW, projectsW} {
		if err := w.Commit(); err != nil {
			return err
		}
	}
	e.logger.Info("saved", "file", filepath.Join(e.outDir, UsersFile), "records", usersW.Count())
	e.logger.Info("saved", "file", filepath.Join(e.outDir, ProjectsFile), "records", projectsW.Count())
	return nil
}

// fetch pages through every workspace, sending users and projects to their
// writers. It always closes both channels, which tells the writers to stop.
func (e *Extractor) fetch(ctx context.Context, usersCh chan<- []client.User, projectsCh chan<- []client.Project) error {
	defer close(usersCh)
	defer close(projectsCh)

	workspaces, err := e.client.ListWorkspaces(ctx)
	if err != nil {
		return fmt.Errorf("list workspaces: %w", err)
	}

	// A user in several workspaces is returned once per workspace; keep one
	// entry (its Workspaces field already lists them all). This set is the
	// only per-record state and holds GIDs only.
	seenUsers := map[string]struct{}{}

	for _, ws := range workspaces {
		var nUsers, nProjects int
		err := e.client.EachUsersPage(ctx, ws.GID, func(page []client.User) error {
			fresh := page[:0] // filter in place; the page is ours
			for _, u := range page {
				if _, dup := seenUsers[u.GID]; !dup {
					seenUsers[u.GID] = struct{}{}
					fresh = append(fresh, u)
				}
			}
			nUsers += len(page)
			return send(ctx, usersCh, fresh)
		})
		if err != nil {
			return fmt.Errorf("list users in workspace %s: %w", ws.GID, err)
		}

		err = e.client.EachProjectsPage(ctx, ws.GID, func(page []client.Project) error {
			nProjects += len(page)
			return send(ctx, projectsCh, page)
		})
		if err != nil {
			return fmt.Errorf("list projects in workspace %s: %w", ws.GID, err)
		}

		e.logger.Debug("fetched workspace", "workspace", ws.Name, "users", nUsers, "projects", nProjects)
	}
	return nil
}

// send hands a page to a writer, giving up if ctx is cancelled (for example
// because that writer already failed and stopped receiving).
func send[T any](ctx context.Context, ch chan<- []T, page []T) error {
	if len(page) == 0 {
		return nil
	}
	select {
	case ch <- page:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// drain writes every record it receives. On the first error it cancels the
// pipeline and keeps receiving (discarding) until the channel closes, so the
// fetcher is never left blocked on a send.
func drain[T any](ch <-chan []T, w snapshotWriter, cancel context.CancelFunc) error {
	var err error
	for page := range ch {
		if err != nil {
			continue
		}
		for _, rec := range page {
			if err = w.Write(rec); err != nil {
				cancel()
				break
			}
		}
	}
	return err
}
