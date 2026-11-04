# kuma-analytics

First milestone of the Go rewrite of the Uptime Kuma backend. It stores raw beats in
TimescaleDB and serves history, charts, percentiles and dashboard stats over REST,
plus new beats over Server-Sent Events.

**Status:** it runs alongside the Node.js backend and does not replace it. Nothing
sends beats to it yet, and checks, notifications, login and the web UI stay in Node.js.

## Run

```bash
export ANALYTICS_API_TOKEN="$(openssl rand -hex 32)"
export POSTGRES_PASSWORD="$(openssl rand -hex 32)"
docker compose up --build
```

| Variable | Default | Purpose |
|---|---|---|
| `DATABASE_URL` | required | PostgreSQL URL of a server with TimescaleDB Community Edition |
| `ANALYTICS_API_TOKEN` | required | Bearer token, at least 32 characters |
| `LISTEN_ADDR` | `127.0.0.1:3002` | Address to listen on |
| `RETENTION_DAYS` | `365` | Raw beat retention, `0` keeps them forever. Aggregates are kept regardless |

The service has plain HTTP and one shared token, so keep it on a private network or
behind a TLS proxy. Browsers can't set headers on `EventSource`, so `/api/v1/events`
is meant for a proxy or server-side client until login moves over.

## API

All routes except `/healthz` need `Authorization: Bearer <token>`.

| Route | Description |
|---|---|
| `GET /healthz` | Database connectivity |
| `POST /api/v1/beats` | Store a JSON array of up to 5,000 beats; safe to replay |
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

Beats are unique per monitor and time, so a retry can't double count them.
Uptime is `up / (up + down)`, where pending counts as down and maintenance is excluded,
matching the Node.js backend.

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
