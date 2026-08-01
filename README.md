# Tempo

Tempo is a lightweight workspace and project-management application built with **SolidJS**, strict TypeScript, and Go. The complete frontend is embedded in one Go executable. MongoDB stores both application state and the admin identity; legacy JSON files are supported only as explicit, one-time migration sources.

## Features

- Multiple workspaces and projects
- Cookie-based admin authentication persisted in MongoDB, with Argon2id for new password hashes
- In-app username, contact email, and password settings with current-password verification
- HttpOnly, SameSite=Strict, signed 8-hour sessions and CSRF-protected mutations
- Login throttling, same-origin enforcement, security headers, and logout
- Project overview with progress, tracked time, open work, focus, and upcoming dates
- Task creation with status, priority, assignee, dates, estimates, and tags
- Centered responsive quick-action dock for creation, search, notifications, and settings
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

`TEMPO_MONGO_URI` is required. On the first start against an empty authentication collection, provide an admin password of at least 12 characters through `TEMPO_ADMIN_PASSWORD` or `TEMPO_ADMIN_PASSWORD_FILE`. Prefer a mode-`0600` password file so the password is not visible in the process list:

```bash
umask 077
read -rsp 'Initial Tempo admin password: ' TEMPO_BOOTSTRAP_PASSWORD
printf '\n'
printf '%s\n' "$TEMPO_BOOTSTRAP_PASSWORD" > /tmp/tempo-admin-password
unset TEMPO_BOOTSTRAP_PASSWORD

TEMPO_MONGO_URI='mongodb://<user>:<password>@127.0.0.1:27017/tempo?authSource=tempo' \
./bin/tempo -addr 127.0.0.1:8080 \
  -admin-password-file /tmp/tempo-admin-password
```

Once the server logs `tempo running`, remove `/tmp/tempo-admin-password` from another terminal. Tempo has already stored the resulting account and session-signing key in MongoDB. Later starts against that authentication collection require neither `TEMPO_ADMIN_PASSWORD` nor `TEMPO_ADMIN_PASSWORD_FILE`; stale bootstrap-file settings are ignored after the Mongo record exists.

Open <http://127.0.0.1:8080>.

Environment configuration for an HTTPS deployment:

```bash
TEMPO_ADDR=:8080 \
TEMPO_MONGO_URI='mongodb://<user>:<password>@127.0.0.1:27017/tempo?authSource=tempo' \
TEMPO_MONGO_DATABASE=tempo \
TEMPO_MONGO_COLLECTION=app_state \
TEMPO_MONGO_AUTH_COLLECTION=auth \
TEMPO_SECURE_COOKIE=true \
./bin/tempo
```

Keep the URI in a mode-`0600` systemd environment file; never commit it or pass it on a command line.

## Build from source

Requirements: Go 1.25+, Node.js 22+, and pnpm 11+.

```bash
make install
make test
make build
```

The build first produces the SolidJS bundle, then embeds `frontend/dist` into the Go binary. Configure MongoDB and start the resulting binary as shown above.

## Development

Start the complete development environment with one command:

```bash
make dev
```

The launcher loads the repository's `.env` when present, requires `TEMPO_MONGO_URI`, performs an initial frontend and Go build, starts the Go API on port 8080, verifies MongoDB health, and starts Vite on port 5173. Open <http://127.0.0.1:5173>. Vite hot-reloads frontend changes, while edits to Go files automatically rebuild and restart the API. Restart the launcher after changing `.env`.

If the configured MongoDB authentication collection is empty and neither bootstrap password option is configured, the launcher securely prompts once. It passes that credential through a temporary mode-`0600` file, deletes the file after MongoDB-backed startup succeeds, and does not retain the plaintext in the server environment. Later `make dev` runs do not prompt again.

To skip loading `.env`, or to use a different environment file:

```bash
TEMPO_LOAD_ENV=false make dev
TEMPO_ENV_FILE=/path/to/dev.env make dev
```

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

Tempo requires MongoDB. It stores the complete, internally consistent application state in one versioned document in `TEMPO_MONGO_COLLECTION` (default `app_state`) and stores the admin identity, password verifier, and session-signing key in `TEMPO_MONGO_AUTH_COLLECTION` (default `auth`). The two collection names must be different, and both documents use `_id: "tempo"`. Authentication writes use majority acknowledgement and are read back from the primary before in-memory credentials change. `/api/health` checks the MongoDB connection.

Normal startup neither reads nor writes JSON. To migrate an older installation, explicitly set `TEMPO_LEGACY_STATE_FILE` and/or `TEMPO_LEGACY_AUTH_FILE`. Each source is imported only when its corresponding MongoDB document is absent. If a document already exists, MongoDB wins and Tempo does not consult that JSON source. After verifying the imported state and a successful login, remove the migration settings and the obsolete JSON files. `TEMPO_DATA` and `TEMPO_AUTH_FILE` remain deprecated aliases for the legacy import settings; do not use them for new deployments.

New accounts and password changes use salted Argon2id hashes; plaintext passwords are never stored. A legacy PBKDF2 verifier is retained when explicitly imported so the existing password continues to work, and the next password change replaces it with Argon2id. Username and password changes rotate the signing key, invalidating older sessions.

Back up both the state and authentication collections with an authenticated `mongodump`, and restore both from the same backup point. Losing the `auth` collection loses the stored login identity and session-signing key even if the project state remains available.

Run only one Tempo server instance for a given database and collection pair. Mutations are serialized inside one process, but the singleton state document does not provide distributed coordination between multiple Tempo instances; concurrent instances can overwrite each other's state updates.

## API

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/health` | Health probe |
| `GET` | `/api/auth/session` | Current login and in-memory CSRF token |
| `POST` | `/api/auth/login` | Authenticate and issue the HttpOnly cookie |
| `POST` | `/api/auth/logout` | Clear the current cookie |
| `PATCH` | `/api/auth/settings` | Update the singleton account and refresh its session |
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
internal/app/mongo_store.go MongoDB state and authentication backend
internal/app/legacy_json.go explicit one-time JSON migration readers
internal/app/auth.go       credentials, cookies, signing, CSRF, throttling
internal/app/server.go     protected REST API, security headers, SPA fallback
frontend/src/App.tsx       SolidJS application and feature views
frontend/src/api.ts        typed API adapter
frontend/src/utils.ts      calendar/time/date logic
qa/run-qa.mjs              production browser workflow
```

## Current deployment boundary

Tempo provides one editable, persistent admin identity and cookie-based session protection. It does **not** yet provide account registration, password recovery, roles, or multi-tenant authorization. For internet exposure, terminate TLS at a trusted reverse proxy and set `TEMPO_SECURE_COOKIE=true` so browsers send the cookie only over HTTPS.
