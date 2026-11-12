// Command asana fetches the projects in every Asana workspace visible to the
// personal access token in ASANA_PERSONAL_ACCESS_TOKEN and saves them to
// <out>/projects.json.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/dumitruPuggle/go-assessment-asana/internal/client"
	"github.com/dumitruPuggle/go-assessment-asana/internal/config"
	"github.com/dumitruPuggle/go-assessment-asana/internal/storage"
)

func main() {
	envFile := flag.String("env", ".env", "path to .env file")
	outDir := flag.String("out", "dist", "directory to write projects.json into")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, *envFile, *outDir); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, envFile, outDir string) error {
	cfg, err := config.Load(envFile)
	if err != nil {
		return err
	}
	c, err := client.New(cfg.AsanaToken)
	if err != nil {
		return err
	}
	return export(ctx, c, outDir)
}

// export fetches users and projects across all workspaces and writes them to
// outDir/users.json and outDir/projects.json. Everything is fetched before
// anything is written, so an API failure never leaves a mix of fresh and
// stale files.
func export(ctx context.Context, c *client.Client, outDir string) error {
	workspaces, err := c.ListWorkspaces(ctx)
	if err != nil {
		return fmt.Errorf("list workspaces: %w", err)
	}

	// Non-nil so empty results encode as [] rather than null.
	users := []client.User{}
	projects := []client.Project{}
	seenUsers := map[string]bool{}

	for _, ws := range workspaces {
		us, err := c.ListUsers(ctx, ws.GID)
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

		ps, err := c.ListProjects(ctx, ws.GID)
		if err != nil {
			return fmt.Errorf("list projects in workspace %s: %w", ws.GID, err)
		}
		projects = append(projects, ps...)

		log.Printf("workspace %q: %d users, %d projects", ws.Name, len(us), len(ps))
	}

	for _, f := range []struct {
		name  string
		data  any
		count int
	}{
		{"users.json", users, len(users)},
		{"projects.json", projects, len(projects)},
	} {
		path := filepath.Join(outDir, f.name)
		if err := storage.WriteJSON(path, f.data); err != nil {
			return err
		}
		log.Printf("saved %d records to %s", f.count, path)
	}
	return nil
}
