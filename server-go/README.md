# kuma-analytics

The Uptime Kuma backend, rewritten in Go and still in progress. It runs HTTP, keyword, TCP and
group monitors, stores raw beats in TimescaleDB, and serves monitor config, history, charts,
percentiles and dashboard stats over REST, plus new beats over Server-Sent Events. It exports
traces and metrics with OpenTelemetry, and ships Grafana dashboards.

**Status:** this is now the only backend, because the Node.js server and MariaDB support have
been removed. It isn't feature complete. Not built yet: login, notifications, maintenance
windows, status pages, tags, TLS certificate tracking, HTTP authentication options, proxies, all
other monitor types, and any import of existing data. The web UI in `src/` can't connect to it yet.

## Run

```bash
export ANALYTICS_API_TOKEN="$(openssl rand -hex 32)"
export POSTGRES_PASSWORD="$(openssl rand -hex 32)"
export GRAFANA_ADMIN_PASSWORD="$(openssl rand -hex 18)"
export GRAFANA_DB_PASSWORD="$(openssl rand -hex 32)"
docker compose up --build
```

| Variable                      | Default          | Purpose                                                                                         |
| ----------------------------- | ---------------- | ----------------------------------------------------------------------------------------------- |
| `DATABASE_URL`                | required         | PostgreSQL URL of a server with TimescaleDB Community Edition                                   |
| `ANALYTICS_API_TOKEN`         | required         | Admin bearer token for managing API keys, at least 32 characters                                |
| `LISTEN_ADDR`                 | `127.0.0.1:3002` | Address to listen on                                                                            |
| `RETENTION_DAYS`              | `365`            | Raw beat retention, `0` keeps them forever. Aggregates are kept regardless                      |
| `CHECK_CONCURRENCY`           | `2000`           | Maximum checks running at once                                                                  |
| `API_KEY_RATE_LIMIT`          | `50`             | Requests per second allowed per API key                                                         |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | not set          | OTLP gRPC endpoint. Without it nothing is exported. Other standard `OTEL_*` variables apply too |
| `KUMA_TRACE_SUCCESS_RATIO`    | `0.01`           | Share of successful traces to export. Failures are always exported                              |
| `GRAFANA_DB_PASSWORD`         | not set          | Creates or updates the read-only `kuma_grafana` database role                                   |

Run a single instance: there's no coordination between schedulers, so two instances
would both run every check.

The service speaks plain HTTP, so keep it on a private network or behind a TLS proxy. Browsers
can't set headers on `EventSource`, so `/api/v1/events` is meant for a proxy or server-side
client until login moves over.

## API

The API is described by an OpenAPI document at `GET /api/v1/openapi.json`, which you can load
into Swagger UI, Postman or a client generator.

### API keys

Create keys with the admin token, `ANALYTICS_API_TOKEN`. Clients should use keys rather than
the admin token, which can do everything and can't be revoked without a restart.

```bash
curl -X POST http://localhost:3002/api/v1/api-keys \
  -H "Authorization: Bearer $ANALYTICS_API_TOKEN" \
  -d '{"name": "ci-pipeline", "scopes": ["monitors:read", "monitors:write"], "expiresAt": "2027-01-01T00:00:00Z"}'
```

The response holds the key, `kuma_...`, and this is the only time you can see it. Only its
SHA-256 hash is stored. List keys with `GET /api/v1/api-keys`, and revoke one with
`DELETE /api/v1/api-keys/{id}`, which takes effect on the next request.

| Scope            | Allows                                                                       |
| ---------------- | ---------------------------------------------------------------------------- |
| `monitors:read`  | List and read monitors, with credentials hidden                              |
| `monitors:write` | Create, change, pause, resume and delete monitors, and see their credentials |
| `beats:read`     | Read beats, charts, percentiles and stats, and stream events                 |
| `beats:write`    | Store beats directly, for importing                                          |

Each key may make `API_KEY_RATE_LIMIT` requests per second (default 50), with short bursts of
four times that. Over the limit, requests get `429` with a `Retry-After` header. The limit
is per server process, and the admin token isn't limited. Changes are logged with the id of the
key that made them, never the key itself.

Keys and the admin token are bearer credentials, so put TLS in front of the API before setting
`API_BIND=0.0.0.0`. A key with `monitors:write` can make this server send requests to any
address it can reach, including internal ones, and read their status codes.

### Managing monitors

```bash
export KEY=kuma_...

# Create a monitor, checked immediately
curl -X POST http://localhost:3002/api/v1/monitors -H "Authorization: Bearer $KEY" \
  -d '{"name": "Website", "type": "http", "url": "https://example.com", "interval": 60}'

# Change some fields, leaving the rest as they are
curl -X PATCH http://localhost:3002/api/v1/monitors/1 -H "Authorization: Bearer $KEY" \
  -d '{"interval": 30, "maxRetries": 2}'

# List, 100 at a time by default. X-Total-Count holds the total across all pages.
curl -i "http://localhost:3002/api/v1/monitors?type=http&active=true&limit=50&offset=0" \
  -H "Authorization: Bearer $KEY"

curl -X POST http://localhost:3002/api/v1/monitors/1/pause -H "Authorization: Bearer $KEY"
curl -X POST http://localhost:3002/api/v1/monitors/1/resume -H "Authorization: Bearer $KEY"
curl -X DELETE http://localhost:3002/api/v1/monitors/1 -H "Authorization: Bearer $KEY"
```

| Route                                                        | Scope                             | Description                                                                                        |
| ------------------------------------------------------------ | --------------------------------- | -------------------------------------------------------------------------------------------------- |
| `GET /healthz`                                               | none                              | Database connectivity                                                                              |
| `GET /api/v1/openapi.json`                                   | none                              | This API's description                                                                             |
| `GET, POST /api/v1/monitors`                                 | `monitors:read`, `monitors:write` | List with `type`, `active`, `parentId`, `limit` and `offset`, or create                            |
| `GET /api/v1/monitors/{id}`                                  | `monitors:read`                   | Read a monitor                                                                                     |
| `PUT /api/v1/monitors/{id}`                                  | `monitors:write`                  | Replace a monitor. Fields left out return to their defaults                                        |
| `PATCH /api/v1/monitors/{id}`                                | `monitors:write`                  | Change only the fields sent                                                                        |
| `DELETE /api/v1/monitors/{id}`                               | `monitors:write`                  | Delete a monitor, its beats and its history                                                        |
| `POST /api/v1/monitors/{id}/pause`, `/resume`                | `monitors:write`                  | Stop or restart checks                                                                             |
| `GET /api/v1/monitors/{id}/beats?limit=100`                  | `beats:read`                      | Newest raw beats, up to 1,000                                                                      |
| `GET /api/v1/monitors/{id}/chart?hours=24`                   | `beats:read`                      | Buckets: 1 min to 6h, 5 min to 24h, hourly to 30d, then daily                                      |
| `GET /api/v1/monitors/{id}/percentiles?hours=168&p=50,95,99` | `beats:read`                      | Ping percentiles of up beats, from raw data                                                        |
| `GET /api/v1/stats`                                          | `beats:read`                      | 24h, 30d and 1y uptime and 24h average ping for all monitors                                       |
| `GET /api/v1/events?monitorId=1`                             | `beats:read`                      | SSE stream of new beats, without replay                                                            |
| `POST /api/v1/beats`                                         | `beats:write`                     | Store up to 5,000 beats; safe to replay. For importing, because the scheduler writes its own beats |

A beat looks like this. Status is 0 down, 1 up, 2 pending or 3 maintenance:

```json
{
  "monitorId": 1,
  "time": "2026-11-04T05:00:00Z",
  "status": 1,
  "ping": 42.5,
  "msg": "200 - OK",
  "important": false,
  "duration": 60,
  "retries": 0
}
```

A failed beat, meaning DOWN or PENDING, also has a `traceId`. Successful beats don't, because
most of their traces aren't kept.

A monitor needs a name and a type. These are the defaults for everything else:

```json
{
  "name": "Site",
  "type": "http",
  "url": "https://example.com",
  "active": true,
  "interval": 60,
  "retryInterval": 0,
  "timeout": 0,
  "maxRetries": 0,
  "upsideDown": false,
  "method": "GET",
  "headers": {},
  "body": "",
  "bodyEncoding": "json",
  "acceptedStatusCodes": ["200-299"],
  "maxRedirects": 10,
  "ignoreTls": false
}
```

Set `parentId` to put a monitor in a group, and `propagateTrace` to send a W3C `traceparent`
header to the target. `group` monitors need no other fields: a group is down if any direct
child is down, and pending if it's empty or a child is pending. Paused children don't count.

`keyword` monitors also take `keyword` and `invertKeyword`. `port` monitors take
`hostname` and `port` instead of the HTTP fields. Times are in seconds, a `timeout` of
0 means 80% of the interval, and a `retryInterval` of 0 means the normal interval.
Retries, upside-down mode and important beats follow the rules of the original Node.js backend.
Beats reach the database in batches, about once a second.

Beats are unique per monitor and time, so a retry can't double count them.
Uptime is `up / (up + down)`, where pending counts as down and maintenance is excluded,
matching the Node.js backend.

## Importing from MariaDB

`kuma-import` copies monitors, groups and history from an Uptime Kuma MariaDB database. It only
reads the source, inside a read-only transaction. Give it a user that can only read, too:

```sql
CREATE USER 'kuma_import'@'%' IDENTIFIED BY '...';
GRANT SELECT ON kuma.* TO 'kuma_import'@'%';
```

It's a dry run unless you pass `-apply`. The report goes to stdout as JSON, and holds no credentials.

```bash
export SOURCE_MARIADB_DSN='kuma_import:password@tcp(host:3306)/kuma'

# See what would happen
docker compose run --rm --no-deps -e SOURCE_MARIADB_DSN --entrypoint /kuma-import analytics

# Import. Monitors arrive paused, so nothing checks twice
docker compose run --rm --no-deps -e SOURCE_MARIADB_DSN --entrypoint /kuma-import analytics -apply

# When ready to run both side by side: resume what was active in the source, then restart
docker compose run --rm --no-deps -e SOURCE_MARIADB_DSN --entrypoint /kuma-import analytics -apply -activate
docker compose restart analytics
```

Check these parts of the report:

- `skipped` lists monitors that weren't imported, and why. Unsupported types and settings are skipped
  because checking without them would show false results. Monitors with basic or bearer authentication are
  imported with an `Authorization` header.
- `droppedSettings` counts settings that are lost but don't change whether a check passes.
- `importedWithNotifications` counts monitors whose alerts don't exist here yet.

You can run it as often as you like. It duplicates nothing and picks up changes, except for monitors that have
been resumed here, which are left alone because they produce their own data. Notifications, tags, status pages,
maintenance windows and users aren't imported.

Uptime before the last whole hour comes from the source's aggregates, and everything after comes from beats, so
nothing counts twice. The same applies per day for daily figures. Dashboards and the API read the `uptime_hourly`
and `uptime_daily` views, which join the two.

## Observability

Grafana is at http://localhost:3003. Log in as `admin` with `GRAFANA_ADMIN_PASSWORD`. Set
`GRAFANA_BIND=0.0.0.0` to open it from other machines. Everything is provisioned from
`observability/`, so change dashboards there rather than in the UI.

| Dashboard        | Shows                                                          | Source             |
| ---------------- | -------------------------------------------------------------- | ------------------ |
| Uptime by group  | Group and monitor uptime, response time, failed checks         | TimescaleDB        |
| Monitor failures | Recent failures, with links from each trace ID to Tempo        | TimescaleDB, Tempo |
| Service health   | Checks, durations, scheduler lag, beat queue, API, SSE, memory | Prometheus         |

Group uptime is the share of successful checks, `up / (up + down)`, across every monitor in
the group, including nested groups. Pending counts as failed, and maintenance doesn't count.
Monitors with short intervals therefore count for more. A monitor in a nested group counts
towards each group above it, and groups' own status beats don't count.

Every trace is recorded, and the service decides which to export when each one ends: all
failures, and a sample of the rest. That way a trace ID on a failed beat always has a trace,
as long as the backend has kept it (30 days here). Spans never contain URLs, query strings,
headers or request bodies, and the `kuma_grafana` database role can't read those columns.

## Test

```bash
docker run -d --rm --name kuma-go-test -p 127.0.0.1:55433:5432 \
  -e POSTGRES_PASSWORD=test timescale/timescaledb:latest-pg17
TEST_DATABASE_URL='postgres://postgres:test@127.0.0.1:55433/postgres' go test -race ./...
```

Database tests are skipped when `TEST_DATABASE_URL` is not set.

## Licensing

Continuous aggregates, compression, retention policies and other features this service
uses need TimescaleDB Community Edition, which is free to self-host but
[can't be sold as a service](https://www.tigerdata.com/docs/about/latest/timescaledb-editions).
Check this before offering it as a hosted product.
