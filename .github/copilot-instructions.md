# Instructions for AI assistants and reviewers

This is Pantheon Lab's fork of Uptime Kuma, maintained on its own and not merged upstream (see `AGENTS.md`). Copilot,
CodeRabbit and other tools that read this file review and change this fork, so what's below describes it, not
upstream.

## Keep these instructions in sync

If a change makes something here wrong (commands, paths, architecture, tests, workflows), update this file in the
same pull request.

## What it is

- **Backend:** a Go service in `server-go/`, on PostgreSQL with TimescaleDB. The Node.js server, SQLite and MariaDB
  support were removed. `kuma-import` copies an old MariaDB install's data across.
- **Frontend:** the Vue 3 single-page app in `src/` (Options API, Vite, Bootstrap 5.3, Font Awesome 6), embedded in the
  Go binary. It talks to the backend through a realtime API: Server-Sent Events from `/api/realtime/stream`, and
  events posted to `/api/realtime/send`, which keep upstream's Socket.IO event names.
- **Docs:** `server-go/README.md` (setup, configuration, API, single sign-on) and `server-go/PORTING.md` (each phase:
  what it did, how it was tested, known limits, and what wasn't tested).

## Layout

```
server-go/
├── cmd/kuma-analytics/   The service: configuration from environment variables
├── cmd/kuma-import/      Copies monitors, history and settings from a MariaDB install
├── internal/store/       PostgreSQL access (pgx) and migrations (migrate.go)
├── internal/ui/          The web UI's API: realtime events, login and single sign-on, status pages
├── internal/api/         The REST API, with API keys
├── internal/mcp/         The MCP server
├── internal/check/       Monitor types
├── internal/notify/      Notification providers
├── internal/e2e/         End-to-end tests; browser/ drives the built image in Chromium
└── charts/               The Helm chart
src/                      The Vue app; strings in src/lang/en.json
test/frontend-test/       Frontend unit tests (node:test, through tsx)
```

## Commands

```bash
npm ci                 # Not npm install
npm run dev            # Vite on :3000, proxying the API to the Go backend on :3001 (KUMA_BACKEND)
npm run lint           # ESLint and Stylelint
npm run fmt            # Prettier
npm test               # Frontend unit tests
npm run build          # The frontend, into dist/
docker build .         # The image: frontend, Go service and all
```

Go, from `server-go/`, with a TimescaleDB for the store's tests:

```bash
TEST_DATABASE_URL='postgres://postgres:test@127.0.0.1:55433/postgres' go test -race ./...
gofmt -l . && go vet ./...
```

## Conventions

- **Go:** errors wrapped with context; messages shown to users through `userErr`; SQL through pgx with parameters,
  never by joining strings with values. Secrets (notification settings, tokens, client secrets) are sealed in the
  database (`internal/store/secrets.go`) and never sent to browsers or written to logs.
- **Migrations** in `internal/store/migrate.go` are appended, never edited, and every statement is idempotent, since
  a failed migration runs again.
- **Realtime events** are admin-only unless listed in `viewerEvents` (`internal/ui/roles.go`). A new method on the UI's
  `Store` interface needs the fake store in `internal/ui/*_test.go` too.
- **Frontend:** 4 spaces, double quotes, semicolons, Prettier's 120 columns; JSDoc on every function and method; new
  logic as TypeScript modules with unit tests; strings added to `src/lang/en.json` only (other languages come from
  Weblate); `v-html` only with DOMPurify; form fields with labels, and controls usable from the keyboard.
- **Workflows:** actions pinned to a commit with the version in a comment, `permissions: {}` at the top and the least
  each job needs, and `persist-credentials: false` on checkouts. zizmor and actionlint should find nothing.
- **Testing:** say what was and wasn't tested. A person tests changes before they reach production.

## Don't

- Open issues or pull requests against `louislam/uptime-kuma`.
- Commit `data/`, `dist/`, `node_modules/`, `private/` or `tmp/`.
- Commit, push or merge without the user's confirmation.
