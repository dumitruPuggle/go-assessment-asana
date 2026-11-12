// Command asana extracts users and projects from every Asana workspace
// visible to ASANA_PERSONAL_ACCESS_TOKEN into <out>/users.json and
// <out>/projects.json.
//
// Usage:
//
//	asana                    # extract once and exit
//	asana -interval=30sec    # extract every 30 seconds until Ctrl-C
//	asana -interval=5min -v  # every 5 minutes, with debug logs
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dumitruPuggle/go-assessment-asana/internal/client"
	"github.com/dumitruPuggle/go-assessment-asana/internal/config"
	"github.com/dumitruPuggle/go-assessment-asana/internal/extract"
	"github.com/dumitruPuggle/go-assessment-asana/internal/scheduler"
)

// intervals are the supported -interval values.
var intervals = map[string]time.Duration{
	"30sec": 30 * time.Second,
	"5min":  5 * time.Minute,
}

func main() {
	envFile := flag.String("env", ".env", "path to .env file")
	outDir := flag.String("out", "dist", "directory to write users.json and projects.json into")
	intervalFlag := flag.String("interval", "", "repeat the extraction: 30sec or 5min (default: run once)")
	verbose := flag.Bool("v", false, "verbose: log scheduling interval and run timings")
	flag.Parse()

	logger := newLogger(*verbose)

	interval, err := parseInterval(*intervalFlag)
	if err != nil {
		logger.Error(err.Error())
		flag.Usage()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, logger, *envFile, *outDir, interval); err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
}

func newLogger(verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

// parseInterval maps the -interval flag to a duration; "" means run once.
func parseInterval(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	if d, ok := intervals[strings.ToLower(strings.TrimSpace(s))]; ok {
		return d, nil
	}
	return 0, fmt.Errorf("invalid -interval %q: must be 30sec or 5min", s)
}

func run(ctx context.Context, logger *slog.Logger, envFile, outDir string, interval time.Duration) error {
	cfg, err := config.Load(envFile)
	if err != nil {
		return err
	}
	// One client for the whole process, so its rate limiter (150 req/min)
	// covers every scheduled run together, not each run separately.
	c, err := client.New(cfg.AsanaToken)
	if err != nil {
		return err
	}
	ex, err := extract.New(c, outDir, logger)
	if err != nil {
		return err
	}

	if interval == 0 {
		return ex.Run(ctx)
	}

	s, err := scheduler.New(interval, ex.Run,
		scheduler.WithName("extract"),
		scheduler.WithLogger(logger),
	)
	if err != nil {
		return err
	}
	return s.Run(ctx)
}
