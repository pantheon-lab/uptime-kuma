# Porting Uptime Kuma to Go

This records what has moved from the removed Node.js server to this service, phase by phase, and what's left. The
reference for behaviour is the Node.js server in git, at `921745bf^`.

Update this file in the same pull request as each phase. Only mark a phase done when it has been tested, and say what
wasn't. The order of the planned phases is a proposal, and should follow what's actually in use: the dry run of
`kuma-import` lists every monitor it can't bring over, and why.

| Phase | Scope                                                           | Status  |
| ----- | --------------------------------------------------------------- | ------- |
| 0     | Core: checks, notifications, history, web UI, login, deployment | Done    |
| 1     | Maintenance windows and public holidays, tags, TLS certificates | Done    |
| 2     | Push, Ping and DNS monitors                                     | Next    |
| 3     | Status pages and badges                                         | Planned |
| 4     | Remaining monitor types                                         | Planned |
| 5     | Accounts and administration                                     | Planned |
| 6     | Integrations                                                    | Planned |

## Phase 0: Core (done)

- HTTP, keyword, TCP port and group monitors, with retries and upside-down mode
- Notifications, except `nostr` and `Webpush`. `apprise` is ported, but its command isn't in the image
- History, charts and uptime stats in TimescaleDB, and a REST API with API keys
- OpenTelemetry metrics and traces, and Grafana dashboards, instead of `/metrics`
- `kuma-import`, which copies monitors, notifications and history from MariaDB
- The web UI embedded in the image, with its login, setup and realtime API
- A Helm chart, and image releases to GHCR and Docker Hub

## Phase 1: Maintenance, tags and certificates (done)

- **Maintenance windows:** all six strategies, worked out from their rules, so a restart doesn't lose a running window.
  A window on a group covers what's below it. Monitors under maintenance aren't checked and don't alert.
- **Public holidays:** recurring windows can skip them or also run on them. 246 bundled calendars for 2024 to 2035, with
  dates admins can add or remove. This is new, and wasn't in the Node.js server.
- **Tags:** tags with values on monitors, in the list, details, filter and settings pages. `kuma-import` copies them.
- **TLS certificates:** the chain on the monitor's page, and expiry alerts. `kuma-import` copies the setting.

Tested with the race detector against TimescaleDB and MariaDB, by a scripted client against the built image, and in
headless Chromium. Certificate alerts were tested against a private CA and a real webhook.

Not tested: viewers in a different timezone from the server, cloning a monitor, and installing on Kubernetes.

Known limits:

- The REST API has no routes for maintenance, tags or certificate details.
- A window can apply up to 10 seconds late, and a monitor shows it at its next scheduled check.
- Countries with regions have only national holidays. Holiday data needs refreshing each year, because governments
  announce changes, and Mainland China's adjusted days off are only right for years already announced.
- A certificate that fails verification makes the check fail, and the page keeps showing the last good one. A monitor
  changed to plain HTTP keeps showing its old certificate. After a redirect, the final host's certificate is shown.

## Phase 2: Push, Ping and DNS (next)

The most commonly used of the missing monitor types, and each is fairly small.

- **Push:** `/api/push/:pushToken`, with its token, and DOWN when a beat is late.
- **Ping:** needs raw network access, or the `ping` command, neither of which the non-root `scratch` image has now. Decide
  how to provide it first, because it also affects the Helm chart.
- **DNS:** resolve types and a chosen server.

## Phase 3: Status pages and badges (planned)

- Public status pages, with incidents, the RSS feed and custom domains
- Linking maintenance windows to status pages, which makes the options in the maintenance form work
- Badges under `/api/badge/:id/...`

This is the largest phase.

## Phase 4: Remaining monitor types (planned)

Do these in the order they're used, and add each to the importer as it's built.

| Category      | Types                                                                               |
| ------------- | ----------------------------------------------------------------------------------- |
| Network       | WebSocket upgrade, NTP, SNMP, SMTP, SIP options, RADIUS, Tailscale ping, Globalping |
| Passive       | Manual                                                                              |
| HTTP variants | JSON query, gRPC keyword, Real browser                                              |
| Databases     | PostgreSQL, MySQL/MariaDB, SQL Server, Oracle, MongoDB, Redis                       |
| Messaging     | MQTT, RabbitMQ, Kafka producer                                                      |
| System        | Docker container, PM2, System service, SFTP                                         |
| Games         | GameDig, Steam                                                                      |

They also need what they depend on: Docker hosts, remote browsers, proxies, the monitor conditions engine, and the
monitor options that are ignored or refused today, namely saved responses, cache busting, IP family, retrying only on
status code failure, and NTLM, mTLS and OAuth logins. Domain expiry tracking belongs here too.

## Phase 5: Accounts and administration (planned)

- Two-factor login, more than one account, the leaked password check, and API keys in the UI
- Settings that take effect, such as the data retention setting, which `RETENTION_DAYS` decides today
- The Monitor History page: clearing heartbeats and statistics, shrinking the database, and the database size
- Importing users, status pages and maintenance windows from MariaDB

## Phase 6: Integrations (planned)

- The MCP server, the Cloudflare tunnel and backup upload
- The update check
- The `nostr` and `Webpush` notification providers, and `apprise` in the image
