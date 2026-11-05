# kuma-analytics

The Uptime Kuma backend, rewritten in Go and still in progress. It runs HTTP, keyword, JSON query,
TCP port, ping, DNS, push, group, manual, WebSocket, PostgreSQL, MySQL/MariaDB, SQL Server, Oracle, MongoDB, Redis,
MQTT, RabbitMQ, Kafka producer, NTP, SNMP, SMTP, SIP, RADIUS and Steam monitors, stores raw beats in TimescaleDB, and serves monitor config, history, charts,
percentiles and dashboard stats over REST, plus new beats over Server-Sent Events. It serves public
status pages and badges, exports traces and metrics with OpenTelemetry, and ships Grafana dashboards.

**Status:** this is now the only backend, because the Node.js server and MariaDB support have
been removed. It isn't feature complete. Not built yet: domain expiry tracking, proxies,
two-factor login, more than one account, and all other monitor types.

[PORTING.md](PORTING.md) records what has moved from the Node.js server, phase by phase, and what's left.

## Web UI

The Docker image, built from the repository root's `Dockerfile`, embeds the web UI from `src/` and serves it
on the same port as the API. `/api/v1/` and `/healthz` are the REST API, `/api/auth/`, `/api/setup` and
`/api/realtime/` are the UI's own API, `/api/push/` receives push monitors' beats, `/status/`,
`/api/status-page/` and `/api/badge/` are public, and every other path is the UI. A plain `go build` leaves the UI out.

On first start, open the UI and create the account. Setup is only offered for **10 minutes after the
service starts**, so that a stranger can't claim a server someone forgot to set up; restart the service if
the window has passed. There's one account, and it can do everything.

What works in the UI: logging in and out, changing the password, the dashboard with live beats, uptime and
response times, the event list, adding, editing, pausing and deleting the monitor types above, charts, notifications (add, edit, test, delete, defaults), maintenance windows with public
holidays, tags, certificate details and expiry alerts, status pages with incidents, badges, and saving settings.

Everything else answers "This isn't supported by this server yet", or shows an empty list where a page asks
on its own (API keys). In particular:

- Saving a monitor with another type, a proxy, or an authentication method other than basic or bearer is refused, so that a check never runs differently from what the form shows. Basic and bearer
  authentication are saved as an `Authorization` header, and show up under Headers when you edit again.
- Form settings this service doesn't have, such as certificate expiry notifications, aren't saved.
- Settings are stored, but apart from the certificate expiry days the service doesn't act on them yet. The timezone comes from `KUMA_TIMEZONE`.
- Changes made through the REST API reach open browsers only when they reload.

Sessions are a random token in an `HttpOnly`, `SameSite=Lax` cookie, `Secure` behind HTTPS (the proxy must
send `X-Forwarded-Proto`). Only a SHA-256 hash of the token is stored. A remembered login lasts 7 days and is
renewed while used, and changing the password ends every other login. Passwords are hashed with scrypt in
better-auth's format, so accounts from the Node.js backend could be imported unchanged; the importer doesn't
do that yet. Logins are limited to 10 a minute per username and 60 a minute overall.

## Certificates

HTTPS monitors record the certificate chain they see, which the UI shows on the monitor's page. It's stored only
when something changes. A certificate that fails verification makes the check fail, so that shows as DOWN.

With **Certificate Expiry Notification** on, the monitor alerts its notifications when a certificate in the chain
is within the days set under **Settings > Notifications**, by default 7, 14 and 21. Each deadline alerts once per
certificate, and replacing the certificate starts again. Roots in the system's trust store don't alert. The option
does nothing with **Ignore TLS/SSL errors** on. Alerts are checked hourly and when the certificate changes, and a
failed send is tried again at the next check.

Unlike the Node.js backend, there's also an alert with 0 days left, and the order of the days setting doesn't matter.

## Push, ping and DNS monitors

**Push** monitors wait for their sender, at `/api/push/<token>?status=up&msg=OK&ping=`, by any HTTP method. `status`
is `up` by default; `down` makes the monitor DOWN straight away, without retries. A monitor that hears nothing for
its interval, plus a second, goes DOWN with "No heartbeat in the time window". The token is the only login, so treat
the URL as a secret: it's hidden from API keys without `monitors:write`. Leave the token empty to have one made, and
editing a monitor keeps it. Each monitor accepts 10 pushes a second, with bursts of 20, and answers 429 beyond that,
so that a looping sender or a leaked URL can't fill the database. The Node.js backend had no limit. After a restart,
an edit or maintenance, the wait starts again, so a healthy sender never shows as late because of the server.

**Ping** monitors send ICMP echo requests, one a second, until `pingCount` replies arrive or the timeout is reached.
Any reply makes them UP, and the beat's ping is the first reply's round trip. They use unprivileged ICMP sockets, so the
image stays non-root without the `NET_RAW` capability, but the host must allow it: the process's group must be in the
`net.ipv4.ping_group_range` sysctl. `compose.yaml` and the Helm chart set it for the container only, to
`0 2147483647`. Kubernetes counts it as a safe sysctl. Elsewhere, run with
`--sysctl net.ipv4.ping_group_range="0 2147483647"`. Without it, the service logs a warning at start and every ping
check fails with a message that says so. Unlike the `ping` command the Node.js backend ran, the packet size is the
data size, and there's no numeric output setting, because there's no output to read.

**DNS** monitors look a record up (A, AAAA, CAA, CNAME, MX, NS, PTR, SOA, SRV or TXT) on a comma-separated list of
resolvers, IP addresses or hostnames, tried in turn until one answers. They're UP if there's at least one record, and
the message and the monitor page's last result are formatted as in the Node.js backend. A name that doesn't exist, or
has no record of the type, is DOWN. With conditions, a monitor is UP only if a record meets them: each address or name,
each TXT string, the issuer of a CAA record, the host of an MX record, the target of an SRV record, the primary name
server of SOA, or the first answer of CNAME.

**JSON query** monitors make the same request as HTTP monitors, then pick a value out of the response with a
[JSONata](https://jsonata.org) expression and compare it with the expected value: `==`, `!=` and `contains` compare
text, and `<`, `<=`, `>` and `>=` numbers. An empty expression compares the whole response. The result must be a single
value, not an array or object. Expressions are JSONata 1.5, from [jsonata-go](https://github.com/blues/jsonata-go),
where Node.js had 2.2: the functions added since, such as `$distinct`, aren't there. Tests compare the results with
Node.js's for a set of common queries.

**Manual** monitors report the status chosen with the form's Up and Down buttons, PENDING until one is chosen. Down
skips retries.

**WebSocket** monitors open a `ws://` or `wss://` connection and close it at once. The close code must be one of the
accepted codes, 1000 (a normal close) by default. Headers, basic and bearer authentication, subprotocols and **Ignore
TLS/SSL errors** apply, and **Ignore Sec-WebSocket-Accept header** accepts servers that send a wrong one.

**PostgreSQL**, **MySQL/MariaDB** and **Redis** monitors connect with a URL, such as
`postgres://user:password@host:5432/db`, `mysql://user:password@host:3306/db` or `redis://:password@host:6379/0`
(`rediss://` for TLS), and close the connection after each check. PostgreSQL and MySQL run the query, `SELECT 1` by
default. A MySQL monitor's **Password** field replaces the password in the URL. MySQL can have conditions on the query's
result, which must then be a single value; its message counts the rows otherwise. Redis sends `PING`. Connection
strings and passwords are hidden from API keys without `monitors:write`, and left out of error messages.

**SQL Server** monitors take an ADO connection string, such as
`Server=host,1433;Database=db;User Id=user;Password=...;TrustServerCertificate=true`, or a `sqlserver://` URL. As with
Node.js, the connection is encrypted unless the string sets `Encrypt`. **Oracle** monitors take Easy Connect,
`host:1521/service`, or a `(DESCRIPTION=...)` descriptor, with the user and password in their own fields, and run
`SELECT 1 FROM DUAL` by default. Both can have conditions, as MySQL can. Oracle uses the pure Go go-ora driver, so no
Oracle client is needed.

**MongoDB** monitors run a command given as JSON, `{"ping": 1}` by default, on the database in the URL, and pass if it
answers `ok`. A JSONata expression can then pick a value from the answer, which must exist, and equal the expected value
if there's one.

**MQTT** monitors subscribe to a topic and wait for the first message, which then must contain the keyword, give the
expected value from a JSONata expression, or meet conditions on the topic, the message, or the expression's value. The
host can start with `mqtt://`, `mqtts://`, `ws://` or `wss://`. A topic that gets no message before the timeout is DOWN, so
pick one with a retained message, or one that's published to more often than the interval.

**RabbitMQ** monitors ask each node's management API, such as `https://node1:15672`, for alarms, and pass when one node
answers without any. **Kafka producer** monitors send the message to the topic, and pass when the brokers accept it.
SASL logins are plain, SCRAM-SHA-256 and SCRAM-SHA-512; AWS IAM isn't supported yet.

**NTP** monitors ask the server for the time, and are DOWN if it's unsynchronized, or its stratum, offset or root
dispersion reaches the thresholds, by default 5, 1000 ms and 500 ms. **SNMP** monitors get one OID with SNMP v1 or v2c
and a community, or v3 with a user and no authentication, as Node.js did, then compare the value as a JSON query does.
**SMTP** monitors connect and greet the server, with SMTPS, required STARTTLS, no STARTTLS, or by default STARTTLS when
it's offered, and send no mail.

**SIP options** monitors send a SIP `OPTIONS` request over UDP, and are UP on `200 OK`. Node.js ran `sipsak` for this;
the request is now made directly, so nothing needs installing. **RADIUS** monitors send an Access-Request with the user,
password and station ids, and are UP on Access-Accept. Requests carry a Message-Authenticator, which servers patched for
BlastRADIUS (CVE-2024-3596), such as FreeRADIUS 3.2.5 and later, require.

**Steam** monitors ask the Steam Web API whether it lists the game server, and need a key under **Settings > General**.

## Status pages

Status pages are made and edited in the web UI, under **Status Pages**, and anyone can see them at
`/status/<slug>`. A page has groups of monitors, incidents, a logo, a theme, custom CSS, a footer, analytics
(Google, Umami, Plausible, Matomo or Rybbit) and an RSS feed at `/status/<slug>/rss`. The page's HTML comes with
its title, description and data in it, so search engines and link previews see it without running JavaScript.

Visitors see each monitor's name, its last 100 beats without their messages, and its 24 hour uptime. A monitor's
address is shown only if **Show Clickable Link** is on, and without any user name and password in it.

- **Custom domains:** a page shows at the root of each domain under **Domain Names**. The domain must reach this
  service: in the Helm chart, list it in `ingress.statusPageHosts`. Behind a proxy that changes the `Host`
  header, turn on **Settings > Reverse Proxy > Trust Proxy**, so that `X-Forwarded-Host` is used.
- **Entry page:** with a status page chosen under **Settings > General**, `/` leads there instead of the
  dashboard.
- **Logos** are stored in the database, so the container needs no volume. They must be PNG, up to 2 MB, and
  are served at `/upload/logo<id>.png`, the same address as the Node.js backend's.
- **Caching:** public answers are kept for up to a minute, and beats for 30 seconds. Changes made in the UI
  clear them at once.

### Badges

Badges are SVG images in the shields.io style, for READMEs and dashboards. Only monitors on a status page have
them: any other monitor shows N/A, so that a badge says nothing about monitors that aren't public. While editing a
status page, a monitor's settings have a badge generator that builds the address.

| Route                                  | Shows                                                          |
| -------------------------------------- | -------------------------------------------------------------- |
| `/api/badge/<id>/status`               | Up, Down, Pending or Maintenance                               |
| `/api/badge/<id>/uptime/<duration>`    | Uptime, by default over 24 hours                               |
| `/api/badge/<id>/ping/<duration>`      | Average response time, by default over 24 hours                |
| `/api/badge/<id>/avg-response/<hours>` | Average response time, up to 720 hours                         |
| `/api/badge/<id>/response`             | The last response time                                         |
| `/api/badge/<id>/cert-exp`             | Days until the certificate expires, or its date with `?date=1` |
| `/api/status-page/<slug>/badge`        | Up, Degraded, Down or Maintenance for the whole page           |

A duration is a number with `m`, `h`, `d`, `w`, `M` (30 days) or `y`, up to 1440 minutes, 720 hours or 365 days. A
number alone is hours. The query options of the Node.js backend work the same: `style` (`flat`, `flat-square`,
`plastic`, `for-the-badge`, `social`), `label`, `labelPrefix`, `labelSuffix`, `prefix`, `suffix`, `color`,
`labelColor`, and the colours and labels of each state. Badges are the same, byte for byte, as badge-maker's.

## Maintenance windows

A monitor under maintenance isn't checked. It makes MAINTENANCE beats, which don't count towards uptime, and
doesn't alert. Going into or out of maintenance is an event, but only a failure afterwards alerts. A window on
a group covers every monitor below it. They're managed in the web UI; the REST API has no routes for them yet.

Strategies are manual, single, cron, and recurring by interval, day of the week or day of the month. A window's
state is worked out from its rules and the time, so a restart during a window changes nothing. It can apply up
to 10 seconds late, and a monitor shows it at its next scheduled check. A running window shows on the status pages
chosen in its form.

### Public holidays

A window with recurring days can treat public holidays specially, using the calendars of one or more countries:

- **Skip public holidays:** it doesn't run on a date that's a holiday in any of them.
- **Also run on public holidays:** it runs on those dates too, at its usual time of day. A cron window can't do
  this, because it has no fixed time of day.

A date is judged in the window's timezone, by the day the window starts. Manual and single windows don't use
holidays.

Calendars are bundled in `internal/holidays/holidays.json.gz`: 246 countries for 2024 to 2035, made from
[python-holidays](https://github.com/vacanza/holidays) by `internal/holidays/generate/generate.py`, which also
says how to refresh them. Hong Kong uses the government's general holidays. Keep in mind:

- Calendars are computed, and governments can announce changes later. Mainland China's adjusted days off are only
  right for the years announced when the data was made, so refresh it each year, or correct dates by hand.
- Countries with regions, such as the United States, the United Kingdom and Australia, have only national holidays.
- Mainland China's make-up working days aren't holidays, so a Saturday window still runs on them.

Under **Settings > Holidays**, admins can add their own dates to a country and remove bundled ones, which can be
restored. Changes apply straight away to every window using that country.

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
| `KUMA_BASE_URL`               | not set          | Public address of the web UI, for links in notifications. Without it, alerts have no link       |
| `KUMA_TIMEZONE`               | `UTC`            | IANA timezone that notifications show, such as `Asia/Hong_Kong`                                 |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | not set          | OTLP gRPC endpoint. Without it nothing is exported. Other standard `OTEL_*` variables apply too |
| `KUMA_TRACE_SUCCESS_RATIO`    | `0.01`           | Share of successful traces to export. Failures are always exported                              |
| `GRAFANA_DB_PASSWORD`         | not set          | Creates or updates the read-only `kuma_grafana` database role                                   |

To deploy on Kubernetes, use the Helm chart in [`charts/uptime-kuma`](charts/uptime-kuma/README.md), which
runs the same stack.

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
| `ANY /api/push/{token}?status=&msg=&ping=`                   | the token                         | A push monitor's beat. Answers `{"ok":true}`, or 404 or 429 with `{"ok":false,"msg":...}`          |
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

### Notifications

Alerts go out when a monitor goes down or recovers, following the same rules as Uptime Kuma. Adding a healthy
monitor sends nothing, a retry sends nothing, and `resendInterval` on a monitor repeats the alert every that
many DOWN beats. Sending happens in the background, so a slow provider can't hold up checks.

```bash
# What's supported
curl -H "Authorization: Bearer $KEY" localhost:3002/api/v1/notifications/providers

# Create one. Config uses the same field names as Uptime Kuma
curl -H "Authorization: Bearer $KEY" localhost:3002/api/v1/notifications -d '{
  "name": "Ops webhook", "type": "webhook", "isDefault": true, "applyToAllMonitors": true,
  "config": {"webhookURL": "https://example.com/hook", "webhookContentType": "json"}
}'

# Send a real test message, which also shows the provider's error
curl -X POST -H "Authorization: Bearer $KEY" localhost:3002/api/v1/notifications/1/test

# Choose what a monitor uses
curl -X PUT -H "Authorization: Bearer $KEY" localhost:3002/api/v1/monitors/1/notifications -d '{"notificationIds": [1]}'
```

`isDefault` attaches a notification to monitors created later, and `applyToAllMonitors` to every existing one.
Keys need `notifications:read` or `notifications:write`. Config can hold credentials, so read-only keys see its
field names but not their values. Config is stored unencrypted in the database, as it was in Uptime Kuma, and
the read-only Grafana role can't read it.

A failed send is tried up to three times if the failure could be temporary, then logged with the beat's trace ID
and counted in the `kuma.notifications` metric, by `provider` and `outcome`. Alerts that are queued or being
retried are lost if the service stops, and up to 1024 can queue per worker before new ones are dropped and
counted. Beats sent to `POST /api/v1/beats` never alert. Monitor URLs in alerts have their query string and
credentials hidden, unlike Uptime Kuma, because alerts go to third parties.

Differences from Uptime Kuma:

- `nostr` and `Webpush` aren't supported.
- `apprise` runs the `apprise` command, which isn't in the container image.
- `NOTIFICATION_PROXY` isn't supported.
- `AlertNow`, `SIGNL4` and `SMSManager` fix bugs: a crash on recovery, an alert ID of `UptimeKuma-undefined`, and
  `gateway=undefined`.

## Importing from MariaDB

`kuma-import` copies monitors, groups, notifications and history from an Uptime Kuma MariaDB database. It only
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

# At the cutover: resume what was active in the source, switch on its notifications, then restart
docker compose run --rm --no-deps -e SOURCE_MARIADB_DSN --entrypoint /kuma-import analytics -apply -activate
docker compose restart analytics
```

Check these parts of the report:

- `skipped` lists monitors that weren't imported, and why. Unsupported types and settings are skipped
  because checking without them would show false results. Monitors with basic or bearer authentication are
  imported with an `Authorization` header.
- `tags` and `tagLinks` count the tags copied and how many were put on monitors. Monitors that were already resumed here keep theirs.
- `droppedSettings` counts settings that are lost but don't change whether a check passes.
- `notifications.skipped` lists notifications that weren't imported, and `notifications.monitorsLosingAlerts`
  counts imported monitors that used one of them. Those monitors won't send that alert here.

You can run it as often as you like. It duplicates nothing and picks up changes, except for monitors that have
been resumed here, which are left alone because they produce their own data. The same goes for notifications
that have been switched on here. Status pages, maintenance windows and users aren't imported yet. Nor is
`primaryBaseURL`, so set `KUMA_BASE_URL` yourself.

Imported notifications stay off until `-activate`, even for monitors you resume by hand, so that a parallel run
doesn't send every alert twice. `-activate` is therefore also when alerts start, and should come when you stop
Uptime Kuma's alerts. Check them with `POST /api/v1/notifications/{id}/test`, which works while they're off.

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

Database tests are skipped when `TEST_DATABASE_URL` is not set. The importer and the MySQL monitor test with
`TEST_MARIADB_DSN`, such as `root:test@tcp(127.0.0.1:53306)/`, and the Redis monitor with `TEST_REDIS_URL`, such as
`redis://:testpass@127.0.0.1:56379/0`. The other monitor types test with these, which the comment in
`internal/check/types4b_test.go` describes: `TEST_MSSQL_DSN`, `TEST_ORACLE` with `TEST_ORACLE_USER` and
`TEST_ORACLE_PASSWORD`, `TEST_MONGODB_URL`, `TEST_MQTT_HOST` and `TEST_KAFKA_BROKER`, and `TEST_SNMP_HOST` and
`TEST_RADIUS`, which `internal/check/network_test.go` describes.

`internal/e2e/browser/` has scripts that drive the web UI in a browser against the built image. Each says what it needs.

## Licensing

Continuous aggregates, compression, retention policies and other features this service
uses need TimescaleDB Community Edition, which is free to self-host but
[can't be sold as a service](https://www.tigerdata.com/docs/about/latest/timescaledb-editions).
Check this before offering it as a hosted product.
