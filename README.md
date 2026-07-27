# Tempo

Tempo is a lightweight workspace and project-management application built with **SolidJS**, strict TypeScript, and Go. The complete frontend is embedded in one Go executable. Runtime state can use atomic JSON persistence or an authenticated MongoDB backend with one-time JSON import.

## Features

- Multiple workspaces and projects
- Cookie-based admin authentication with persistent PBKDF2 credentials
- HttpOnly, SameSite=Strict, signed 8-hour sessions and CSRF-protected mutations
- Login throttling, same-origin enforcement, security headers, and logout
- Project overview with progress, tracked time, open work, focus, and upcoming dates
- Task creation with status, priority, assignee, dates, estimates, and tags
- Desktop, touch, and keyboard-accessible drag-and-drop Kanban board
- Persistent one-at-a-time task timer and per-task/project totals
- Six-week project timeline
- Responsive monthly calendar
- Mobile navigation and layouts
- Seeded first-run workspace so the application is useful immediately
- No frontend runtime server, external font CDN, or client framework beyond SolidJS

## Production quick start

The verified Linux binary is available at:

```text
bin/tempo
```

Run it:

On the first run, provide a password of at least 12 characters. Prefer a mode-`0600` password file (or the environment variable) so the password is not visible in the process list:

```bash
umask 077
openssl rand -base64 24 > /tmp/tempo-admin-password
./bin/tempo -addr 127.0.0.1:8080 -data data/tempo.json \
  -admin-password-file /tmp/tempo-admin-password
rm -f /tmp/tempo-admin-password
```

Tempo creates `data/tempo.json.auth.json` with mode `0600`. Later starts load that credential file and do not require the bootstrap password.

Open <http://127.0.0.1:8080>. The data directory is created automatically.

Environment equivalents for an HTTPS deployment:

```bash
TEMPO_ADDR=:8080 \
TEMPO_DATA=/var/lib/tempo/tempo.json \
TEMPO_AUTH_FILE=/var/lib/tempo/auth.json \
TEMPO_SECURE_COOKIE=true \
./bin/tempo
```

To use MongoDB, set a protected connection URI. `TEMPO_DATA` becomes the one-time import source when the Mongo collection is empty; subsequent starts load MongoDB and never overwrite it from JSON.

```bash
TEMPO_MONGO_URI='mongodb://<user>:***@127.0.0.1:27017/tempo?authSource=tempo' \
TEMPO_MONGO_DATABASE=tempo \
TEMPO_MONGO_COLLECTION=app_state \
TEMPO_DATA=/var/lib/tempo/tempo.json \
./bin/tempo
```

Keep the URI in a mode-`0600` systemd environment file; never commit it or pass it on a command line.

## Build from source

Requirements: Go 1.23+, Node.js 22+, and pnpm 11+.

```bash
make install
make test
make build
./bin/tempo
```

The build first produces the SolidJS bundle, then embeds `frontend/dist` into the Go binary.

## Development

Terminal 1:

```bash
go run . -addr 127.0.0.1:8080 -data /tmp/tempo-dev.json
```

Terminal 2:

```bash
cd frontend
pnpm dev
```

Vite runs at <http://127.0.0.1:5173> and proxies `/api` to Go on port 8080.

## Quality gates

```bash
cd frontend
pnpm typecheck
pnpm test
pnpm build
cd ..
gofmt -w main.go internal/app/*.go
go test ./...
go test -race ./...
go build -trimpath -ldflags='-s -w' -o bin/tempo .
```

Browser QA is in `qa/run-qa.mjs`. It exercises rejected and accepted login, authenticated task creation, persisted HTML/pointer/keyboard Kanban movement, workspace isolation, timer start/stop, mobile login/calendar navigation, and logout before saving screenshots under `qa/`. Supply its temporary password through `TEMPO_QA_PASSWORD` or a mode-`0600` file through `TEMPO_QA_PASSWORD_FILE`.

## Persistence and backups

Tempo serializes mutations under a read/write mutex. JSON mode writes and syncs a temporary file before atomically renaming it over the configured state file. MongoDB mode stores the complete, internally consistent state in one versioned `app_state` document and checks MongoDB in `/api/health`.

When MongoDB is enabled and its state document is absent, Tempo imports `TEMPO_DATA` exactly once. If the document already exists, MongoDB always wins and the JSON source is not reapplied. Preserve the JSON file as a rollback backup after migration. Back up MongoDB with authenticated `mongodump`; restore into an empty collection before startup.

Authentication data is stored separately in `<state-file>.auth.json` by default. It contains a salt, PBKDF2-HMAC-SHA256 password hash, iteration count, and random session-signing key—never the plaintext password. To reset the admin credential, stop Tempo, remove only the auth file, and restart once with a new `TEMPO_ADMIN_PASSWORD`. Existing sessions become invalid.

## API

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/health` | Health probe |
| `GET` | `/api/auth/session` | Current login and in-memory CSRF token |
| `POST` | `/api/auth/login` | Authenticate and issue the HttpOnly cookie |
| `POST` | `/api/auth/logout` | Clear the current cookie |
| `GET` | `/api/state` | Complete workspace state |
| `POST` | `/api/workspaces` | Create workspace |
| `POST` | `/api/projects` | Create project |
| `POST` | `/api/tasks` | Create task |
| `PATCH` | `/api/tasks/{id}` | Update task or Kanban status |
| `POST` | `/api/time/start` | Start a task timer |
| `POST` | `/api/time/stop` | Stop the active timer |

Everything except health, login, and session inspection requires a valid signed cookie. All authenticated state-changing requests also require the session-specific `X-CSRF-Token` header.

## Architecture

```text
main.go                    embedded assets + production HTTP server
internal/app/model.go      domain model
internal/app/store.go      validated, serialized domain operations
internal/app/persistence.go atomic JSON backend
internal/app/mongo_store.go authenticated MongoDB backend + one-time import
internal/app/auth.go       credentials, cookies, signing, CSRF, throttling
internal/app/server.go     protected REST API, security headers, SPA fallback
frontend/src/App.tsx       SolidJS application and feature views
frontend/src/api.ts        typed API adapter
frontend/src/utils.ts      calendar/time/date logic
qa/run-qa.mjs              production browser workflow
```

## Current deployment boundary

Tempo provides one persistent admin identity and cookie-based session protection. It does **not** yet provide account registration, password recovery, roles, or multi-tenant authorization. For internet exposure, terminate TLS at a trusted reverse proxy and set `TEMPO_SECURE_COOKIE=true` so browsers send the cookie only over HTTPS.
