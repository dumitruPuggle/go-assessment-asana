# go-assessment-asana

A small Go command that exports the **users** and **projects** from every Asana
workspace your personal access token can see. It writes them as JSON snapshots
to `users.json` and `projects.json`. It can run once or keep running on a fixed
interval.

---

## Getting started

### Prerequisites

- Go **1.27.3** or newer (see `go.mod`)
- An Asana [personal access token](https://developers.asana.com/docs/personal-access-token)

### 1. Provide the token

The program reads `ASANA_PERSONAL_ACCESS_TOKEN`. You can set it in one of two
ways.

**Option A: a `.env` file** in the directory you run from (git ignores it).
[`.env.example`](.env.example) is a committed template with dummy values that lists
every required secret:

```sh
cp .env.example .env   # then replace the dummy token with your real one
```

```dotenv
ASANA_PERSONAL_ACCESS_TOKEN=1/1234567890:abcdef...
```

| Variable | Required | Description |
|----------|----------|-------------|
| `ASANA_PERSONAL_ACCESS_TOKEN` | yes | Asana personal access token used as the `Bearer` credential. |

**Option B: the environment**:

```sh
export ASANA_PERSONAL_ACCESS_TOKEN=1/1234567890:abcdef...
```

If both are set, the **environment variable wins**. The `.env` file is
optional, and if it is missing the program just uses the environment.

### 2. Run it

```sh
# Extract once into ./dist and exit
go run ./cmd/asana

# Or build a binary first
go build -o asana ./cmd/asana
./asana
```

On success you get:

```
dist/
├── users.json
└── projects.json
```

### 3. Keep it running (optional)

```sh
./asana -interval=30sec        # refresh every 30 seconds
./asana -interval=5min -v      # refresh every 5 minutes, with debug logs
```

Stop it with `Ctrl-C` (SIGINT) or SIGTERM. It shuts down cleanly. A run that is
in progress is cancelled, and the previous output files stay intact.

---

## Command-line arguments

| Flag        | Default | Description |
|-------------|---------|-------------|
| `-env`      | `.env`  | Path to the `.env` file to load. A missing file is not an error. |
| `-out`      | `dist`  | Directory where `users.json` and `projects.json` are written. It is created if it doesn't exist. |
| `-interval` | *(empty)* | Repeat the extraction. Accepted values are `30sec` and `5min` (case-insensitive). Leave it empty to run once and exit. |
| `-v`        | `false` | Verbose mode. Logs at debug level: scheduling interval, per-run timings, per-workspace counts and skipped runs. |
| `-h`        |         | Print usage. |

Examples:

```sh
./asana -env=/etc/asana/prod.env -out=/var/lib/asana
./asana -interval=5min -out=./snapshots -v
```

### Exit codes

| Code | Meaning |
|------|---------|
| `0`  | Success. In interval mode, this means a clean shutdown on a signal. |
| `1`  | Runtime error. For example, the token is missing, the `.env` file is malformed, an API call failed in run-once mode, or a write failed. |
| `2`  | Invalid flags. For example, an unsupported `-interval` value. |

In interval mode a failed run is **logged and the schedule continues**. It
doesn't exit the program.

### Logging

Logs are written to **stderr** in `log/slog` text format. Info level reports
each saved file and its record count. Debug level (`-v`) adds scheduling
details.

```
level=INFO msg=saved file=dist/users.json records=42
level=INFO msg=saved file=dist/projects.json records=17
```

---

## Output format

Both files share the same envelope:

```json
{
  "lastUpdatedAt": 1762951234,
  "results": [ ... ]
}
```

- `lastUpdatedAt` is the Unix time in seconds when the data was fetched. Both
  files from the same run carry the same value.
- `results` is always an array. An empty result is `[]`, never `null`.

**`users.json`**: users are de-duplicated across workspaces. A user who belongs
to several workspaces appears once, and those workspaces are listed in
`workspaces`.

```json
{
  "gid": "1201234567890",
  "name": "Jane Doe",
  "email": "jane@example.com",
  "workspaces": [{ "gid": "1209876543210", "name": "Acme" }]
}
```

**`projects.json`**:

```json
{
  "gid": "1205555555555",
  "name": "Website relaunch",
  "archived": false,
  "workspace": { "gid": "1209876543210", "name": "Acme" }
}
```

> `dist/` contains names and emails and is git-ignored. Don't commit it.

### Sample run (real output)

This is the actual output of `./asana` against a personal Asana workspace
("My workspace": 2 users, 4 projects), captured on 2026-11-12 at 12:44:53 UTC
(`lastUpdatedAt: 1794487493`).

**`dist/users.json`**

```json
{
  "lastUpdatedAt": 1794487493,
  "results": [
    {
      "gid": "1220475969734486",
      "name": "lapeye9007@nbasweeps.com",
      "email": "lapeye9007@nbasweeps.com",
      "workspaces": [
        {
          "gid": "1220472063194862",
          "name": "My workspace"
        }
      ]
    },
    {
      "gid": "1220472063194842",
      "name": "Dumitru Cucu",
      "email": "dumitruiurie@gmail.com",
      "workspaces": [
        {
          "gid": "1220472063194862",
          "name": "My workspace"
        }
      ]
    }
  ]
}
```

**`dist/projects.json`**

```json
{
  "lastUpdatedAt": 1794487493,
  "results": [
    {
      "gid": "1220472333496222",
      "name": "Dumitru's first project",
      "archived": false,
      "workspace": {
        "gid": "1220472063194862",
        "name": "My workspace"
      }
    },
    {
      "gid": "1220473676130453",
      "name": "Выявление ошибок",
      "archived": false,
      "workspace": {
        "gid": "1220472063194862",
        "name": "My workspace"
      }
    },
    {
      "gid": "1220473676130501",
      "name": "Kanban board",
      "archived": false,
      "workspace": {
        "gid": "1220472063194862",
        "name": "My workspace"
      }
    },
    {
      "gid": "1220473676130505",
      "name": "Project timeline",
      "archived": false,
      "workspace": {
        "gid": "1220472063194862",
        "name": "My workspace"
      }
    }
  ]
}
```

---

## Development

```sh
go test ./...                                          # unit tests (offline)
go test -race ./...                                    # with the race detector
go test -tags integration ./internal/client/ -v        # live tests against Asana
go vet ./...
```

The integration tests read the token from the environment or from the
repository-root `.env`. They are skipped if no token is available.

Contributors and coding agents follow the rules in [`AGENTS.md`](AGENTS.md).
`CLAUDE.md` imports the same file. The main rule is that this README is updated
in the same change as any significant design or code change.

### Project layout

```
cmd/asana/            CLI entry point: flags, logger, signals, wiring
internal/client/      Asana REST client (pagination, rate limiting, errors)
internal/config/      Settings from the environment and .env (godotenv)
internal/extract/     One extraction run: fetch everything, then write snapshots
internal/scheduler/   Fixed-interval runner with no overlap and skipped catch-up
internal/storage/     Atomic JSON file writes
```

Data flow:

```
main ──► config.Load ──► client.New ──► extract.New
                                            │
              interval == 0 ──► Extractor.Run (once)
              interval  > 0 ──► scheduler.Run(Extractor.Run)
                                            │
                     client: workspaces ► users + projects per workspace
                                            │
                     storage.WriteJSON ► users.json, projects.json
```

A **single `client.Client`** lives for the whole process. Its rate limiter
therefore covers every scheduled run together, not each run separately.

---

## Package reference

### `internal/client`

A minimal Asana REST API client built on `net/http`. It has no third-party
dependencies.

**Construction**

```go
c, err := client.New(token,
    client.WithBaseURL("https://app.asana.com/api/1.0"), // default; override in tests
    client.WithHTTPClient(&http.Client{Timeout: 30 * time.Second}),
    client.WithPageSize(100),                   // 1–100, default 100
    client.WithRateLimit(150, time.Minute),     // default 150 req/min
)
```

`New` rejects an empty token. Each option validates its own input. For example,
the base URL must be absolute, the HTTP client must be non-nil and the page size
must be in range.

**Resources** (`resources.go`)

| Method | Endpoint | Notes |
|--------|----------|-------|
| `ListWorkspaces(ctx)` | `GET /workspaces` | All workspaces visible to the token. |
| `ListUsers(ctx, wsGID)` | `GET /users?workspace=…` | `opt_fields=name,email,workspaces.name` |
| `EachUsersPage(ctx, wsGID, fn)` | same | Streams one page at a time, so memory stays constant. |
| `ListProjects(ctx, wsGID)` | `GET /projects?workspace=…` | `opt_fields=name,archived,workspace.name` |
| `EachProjectsPage(ctx, wsGID, fn)` | same | Streams one page at a time. |

The `List*` / `Each*` methods return an error if the workspace GID is empty.
Returning an error from an `Each*` callback stops pagination and returns that
error. The types are `Workspace`, `User` and `Project`, and their JSON tags match
the output format above.

**Pagination.** Asana uses offset pagination. Each response's
`next_page.offset` is sent back as `?offset=`, and a `null` or absent
`next_page` marks the last page. As a safety net, if the server ever returns an
offset it has already returned, the client fails instead of looping forever.

**Rate limiting.** A **sliding-window** limiter guarantees that no more than
*n* requests *start* within any rolling window. Unlike a token bucket with a
burst of *n*, it can never let 2*n* requests through around a window boundary.
It keeps a ring buffer of the last *n* start times, and a new request waits
until `window` has passed since the slot it replaces. Slots are reserved under
a mutex before sleeping, so concurrent callers queue fairly. The wait respects
context cancellation.

**Errors.** A non-2xx response becomes an `*APIError`:

```go
type APIError struct {
    StatusCode int
    Messages   []string      // from Asana's {"errors":[{"message":…}]}
    RetryAfter time.Duration // parsed from Retry-After on 429
}
```

Use `errors.As(err, &apiErr)` to inspect it. The client does **not** retry
automatically. `RetryAfter` is available to callers that want to retry.

---

### `internal/config`

Loads runtime settings.

```go
cfg, err := config.Load(".env")
// cfg.AsanaToken
```

- The `.env` file is parsed with
  [`github.com/joho/godotenv`](https://github.com/joho/godotenv). That parser
  supports comments, `export` prefixes, quoted and multi-line values, and
  escapes.
- Variables that are already set in the process environment are **never
  overridden**.
- A missing `.env` file is not an error. A malformed one is.
- `Load` fails if `ASANA_PERSONAL_ACCESS_TOKEN` (`config.TokenEnvVar`) is empty
  after loading. Surrounding whitespace is trimmed.

---

### `internal/extract`

Performs one complete extraction.

```go
ex, err := extract.New(c, "dist", logger) // logger may be nil
err = ex.Run(ctx)
```

`Run`:

1. Lists all workspaces.
2. For each workspace, lists its users and projects.
3. De-duplicates users by GID, because a user in several workspaces is returned
   once per workspace.
4. Stamps both snapshots with a single `lastUpdatedAt`.
5. Writes `users.json` and then `projects.json` through `storage.WriteJSON`.

**Everything is fetched before anything is written.** An API failure in the
middle of a run therefore never leaves one file fresh and the other stale. The
previous snapshots stay untouched. Errors are wrapped with context, for example
`list users in workspace 123: …`.

Exports: `UsersFile`, `ProjectsFile` and the generic `Snapshot[T]` envelope
type.

The `Extractor` is designed to be reused across scheduled runs, so the
underlying client and its rate limiter are shared.

---

### `internal/scheduler`

Runs a `Job func(ctx) error` at a fixed interval.

```go
s, err := scheduler.New(5*time.Minute, ex.Run,
    scheduler.WithName("extract"),
    scheduler.WithLogger(logger),
)
err = s.Run(ctx) // blocks until ctx is cancelled; returns nil
```

Guarantees:

- **Immediate first run**, then one run on every interval boundary. Boundaries
  are anchored to the start time, so they don't drift with run duration.
- **No overlap.** The next run starts only after the previous one returns.
- **Missed runs are skipped, not replayed.** If a run overruns one or more
  boundaries, the scheduler jumps to the next future boundary in one step. A
  slow, rate-limited run therefore can't snowball into a burst of back-to-back
  runs.
- **No busy loop.** The sleep between runs is always strictly positive.
- **Resilient.** A job error or panic is logged at Error level and the schedule
  continues.
- **Prompt shutdown.** Cancelling the context interrupts both the sleep and a
  context-aware job, and `Run` returns `nil`.

`New` rejects a non-positive interval and a nil job. The clock and sleep
functions are injectable inside the package, so the tests run deterministically
without real waiting.

---

### `internal/storage`

```go
err := storage.WriteJSON("dist/users.json", v)
```

Writes `v` as indented JSON:

- Creates parent directories (`0755`) as needed.
- Writes are **atomic**. Data goes to a temp file in the same directory
  (`.users.json.<random>.tmp`), which is `fsync`ed, set to `0644` and then
  `rename`d over the target. Readers never see a partial file. If a write
  fails, the temp file is removed and the previous file is preserved.
