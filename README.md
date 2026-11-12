# go-assessment-asana

This tool exports the users and projects from your Asana workspaces into two
JSON files. You can run it once, or leave it running so it refreshes the files
every 30 seconds or every 5 minutes.

## Getting started

You need Go 1.27.3 or newer and an Asana
[personal access token](https://developers.asana.com/docs/personal-access-token).

### 1. Add your token

Copy the example file and put your token in it:

```sh
cp .env.example .env
```

```dotenv
ASANA_PERSONAL_ACCESS_TOKEN=your-token-here
```

`.env` is git-ignored, so your token stays on your machine. You can also skip
the file and export the variable in your shell instead. If you do both, the
shell variable wins.

`ASANA_PERSONAL_ACCESS_TOKEN` is the only setting the tool needs.

### 2. Run it

```sh
go run ./cmd/asana
```

Or build it first:

```sh
go build -o asana ./cmd/asana
./asana
```

When it's done, you'll find the results in `dist/`:

```
dist/
├── users.json
└── projects.json
```

### 3. Keep it running (optional)

```sh
./asana -interval=30sec
./asana -interval=5min -v
```

Press `Ctrl-C` to stop it. If you stop it in the middle of a run, your previous
files are left as they were.

## Command-line options

| Option | Default | What it does |
|--------|---------|--------------|
| `-env` | `.env` | The `.env` file to read. It's fine if the file doesn't exist. |
| `-out` | `dist` | The folder to write the JSON files to. It's created if needed. |
| `-interval` | none | Run repeatedly: `30sec` or `5min`. Leave it out to run once. |
| `-v` | off | Show extra detail, such as how long each run took. |
| `-h` | | Show help. |

For example:

```sh
./asana -env=/etc/asana/prod.env -out=/var/lib/asana
./asana -interval=5min -out=./snapshots -v
```

The tool exits with `0` on success, `1` if something went wrong (for example,
no token or an API error), and `2` if an option is invalid. When it runs on an
interval, a failed run is logged and it simply tries again next time.

## What the files look like

Both files have the same shape:

```json
{
  "lastUpdatedAt": 1762951234,
  "results": [ ... ]
}
```

`lastUpdatedAt` is when the run finished fetching from Asana, in Unix seconds.
Both files from the same run share the same value. `results` is always a list, even when it's
empty.

Each user appears once, even if they belong to several workspaces. Their
workspaces are listed inside the record:

```json
{
  "gid": "1201234567890",
  "name": "Jane Doe",
  "email": "jane@example.com",
  "workspaces": [{ "gid": "1209876543210", "name": "Acme" }]
}
```

A project looks like this:

```json
{
  "gid": "1205555555555",
  "name": "Website relaunch",
  "archived": false,
  "workspace": { "gid": "1209876543210", "name": "Acme" }
}
```

`dist/` holds real names and emails, so it's git-ignored. Please don't commit
it.

### Example from a real run

Here's what a real run against a personal workspace produced on
2026-11-12 at 12:44:53 UTC: 2 users and 4 projects.

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

## Development

Run the tests:

```sh
go test ./...
go test -race ./...
go vet ./...
```

There are also live tests that call the real Asana API. They use your token
from `.env` and are skipped if there isn't one:

```sh
go test -tags integration ./internal/client/ -v
```

### How the code is organized

```
cmd/asana/            the command: reads options, starts a run or the schedule
internal/config/      reads the token from the environment or .env (godotenv)
internal/client/      talks to the Asana API, follows pages, stays under 150 requests/min
internal/extract/     one run: fetches users and projects, then writes both files
internal/scheduler/   repeats a run on a fixed interval
internal/storage/     writes JSON files safely
```

A few choices worth knowing about:

- **Simple over streaming.** Each run keeps all users and projects in memory,
  then writes each file in one go. That's simple and fast for workspaces of a
  normal size. A page-by-page streaming writer was tried and removed because it
  added more complexity than this project needs. Memory grows with the number
  of records, so very large workspaces (around a million records) would need it
  back.
- **Fetch everything, then write.** Nothing is written until every Asana
  request in the run has succeeded, so a failed request leaves both old files
  as they were. Each file is written to a temporary file and then renamed into
  place, so anyone reading it sees either the old version or the new one, never
  half of either. `users.json` is replaced first, then `projects.json`. If
  writing `projects.json` fails (for example, the disk is full), `users.json`
  will already be new.
- **One rate limit for the whole process.** Asana allows 150 requests per
  minute. The tool uses one API client for its whole lifetime, so scheduled
  runs share that budget. The limiter counts requests over any rolling minute,
  so it never sends a burst across a minute boundary.
- **No pile-ups.** If a run takes longer than the interval, the missed runs are
  skipped instead of running back to back. Runs never overlap.
- **No automatic retries.** If Asana rejects a request, the run fails and the
  next scheduled run tries again.

Contributors and coding agents follow the rules in [`AGENTS.md`](AGENTS.md),
which `CLAUDE.md` also loads. The main rule: update this README whenever you
make a significant design or code change.
