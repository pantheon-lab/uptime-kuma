# kuma-analytics

The Go rewrite of the Uptime Kuma backend, in progress. It runs HTTP, keyword, TCP and
group monitors, stores raw beats in TimescaleDB, and serves monitor config, history, charts,
percentiles and dashboard stats over REST, plus new beats over Server-Sent Events. It exports
traces and metrics with OpenTelemetry, and ships Grafana dashboards.

**Status:** it runs on its own, next to the Node.js backend, and doesn't share data
with it. There's no way to import existing monitors yet, and these still exist only in
Node.js: the web UI, login, notifications, maintenance windows, status pages, tags,
TLS certificate tracking, HTTP authentication options, proxies and every other monitor type.

## Run

```bash
export ANALYTICS_API_TOKEN="$(openssl rand -hex 32)"
export POSTGRES_PASSWORD="$(openssl rand -hex 32)"
export GRAFANA_ADMIN_PASSWORD="$(openssl rand -hex 18)"
export GRAFANA_DB_PASSWORD="$(openssl rand -hex 32)"
docker compose up --build
```

| Variable | Default | Purpose |
|---|---|---|
| `DATABASE_URL` | required | PostgreSQL URL of a server with TimescaleDB Community Edition |
| `ANALYTICS_API_TOKEN` | required | Bearer token, at least 32 characters |
| `LISTEN_ADDR` | `127.0.0.1:3002` | Address to listen on |
| `RETENTION_DAYS` | `365` | Raw beat retention, `0` keeps them forever. Aggregates are kept regardless |
| `CHECK_CONCURRENCY` | `2000` | Maximum checks running at once |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | not set | OTLP gRPC endpoint. Without it nothing is exported. Other standard `OTEL_*` variables apply too |
| `KUMA_TRACE_SUCCESS_RATIO` | `0.01` | Share of successful traces to export. Failures are always exported |
| `GRAFANA_DB_PASSWORD` | not set | Creates or updates the read-only `kuma_grafana` database role |

Run a single instance: there's no coordination between schedulers, so two instances
would both run every check.

The service has plain HTTP and one shared token, so keep it on a private network or
behind a TLS proxy. Browsers can't set headers on `EventSource`, so `/api/v1/events`
is meant for a proxy or server-side client until login moves over.

## API

All routes except `/healthz` need `Authorization: Bearer <token>`.

| Route | Description |
|---|---|
| `GET /healthz` | Database connectivity |
| `GET, POST /api/v1/monitors` | List monitors, or create one |
| `GET, PUT, DELETE /api/v1/monitors/{id}` | Read, replace or delete a monitor. Delete also removes its beats and history |
| `POST /api/v1/beats` | Store a JSON array of up to 5,000 beats; safe to replay. For importing, because the scheduler writes its own beats |
| `GET /api/v1/monitors/{id}/beats?limit=100` | Newest raw beats, up to 1,000 |
| `GET /api/v1/monitors/{id}/chart?hours=24` | Buckets: 1 min to 6h, 5 min to 24h, hourly to 30d, then daily |
| `GET /api/v1/monitors/{id}/percentiles?hours=168&p=50,95,99` | Ping percentiles of up beats, from raw data |
| `GET /api/v1/stats` | 24h, 30d and 1y uptime and 24h average ping for all monitors |
| `GET /api/v1/events?monitorId=1` | SSE stream of new beats, without replay |

A beat looks like this. Status is 0 down, 1 up, 2 pending or 3 maintenance:

```json
{"monitorId": 1, "time": "2026-11-04T05:00:00Z", "status": 1, "ping": 42.5,
 "msg": "200 - OK", "important": false, "duration": 60, "retries": 0}
```

A failed beat, meaning DOWN or PENDING, also has a `traceId`. Successful beats don't, because
most of their traces aren't kept.

A monitor needs a name and a type. These are the defaults for everything else:

```json
{"name": "Site", "type": "http", "url": "https://example.com", "active": true,
 "interval": 60, "retryInterval": 0, "timeout": 0, "maxRetries": 0, "upsideDown": false,
 "method": "GET", "headers": {}, "body": "", "bodyEncoding": "json",
 "acceptedStatusCodes": ["200-299"], "maxRedirects": 10, "ignoreTls": false}
```

Set `parentId` to put a monitor in a group, and `propagateTrace` to send a W3C `traceparent`
header to the target. `group` monitors need no other fields: a group is down if any direct
child is down, and pending if it's empty or a child is pending. Paused children don't count.

`keyword` monitors also take `keyword` and `invertKeyword`. `port` monitors take
`hostname` and `port` instead of the HTTP fields. Times are in seconds, a `timeout` of
0 means 80% of the interval, and a `retryInterval` of 0 means the normal interval.
Retries, upside-down mode and important beats follow the Node.js backend's rules.
Beats reach the database in batches, about once a second.

Beats are unique per monitor and time, so a retry can't double count them.
Uptime is `up / (up + down)`, where pending counts as down and maintenance is excluded,
matching the Node.js backend.

## Observability

Grafana is at http://localhost:3003. Log in as `admin` with `GRAFANA_ADMIN_PASSWORD`. Set
`GRAFANA_BIND=0.0.0.0` to open it from other machines. Everything is provisioned from
`observability/`, so change dashboards there rather than in the UI.

| Dashboard | Shows | Source |
|---|---|---|
| Uptime by group | Group and monitor uptime, response time, failed checks | TimescaleDB |
| Monitor failures | Recent failures, with links from each trace ID to Tempo | TimescaleDB, Tempo |
| Service health | Checks, durations, scheduler lag, beat queue, API, SSE, memory | Prometheus |

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
