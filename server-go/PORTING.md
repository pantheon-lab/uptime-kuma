# Porting Uptime Kuma to Go

This records what has moved from the removed Node.js server to this service, phase by phase, and what's left. The
reference for behaviour is the Node.js server in git, at `921745bf^`.

Update this file in the same pull request as each phase. Only mark a phase done when it has been tested, and say what
wasn't. The order of the planned phases is a proposal, and should follow what's actually in use: the dry run of
`kuma-import` lists every monitor it can't bring over, and why.

| Phase | Scope                                                               | Status  |
| ----- | ------------------------------------------------------------------- | ------- |
| 0     | Core: checks, notifications, history, web UI, login, deployment     | Done    |
| 1     | Maintenance windows and public holidays, tags, TLS certificates     | Done    |
| 2     | Push, Ping and DNS monitors                                         | Done    |
| 3     | Status pages and badges                                             | Done    |
| 4a    | Conditions, JSON query, manual, WebSocket, PostgreSQL, MySQL, Redis | Done    |
| 4b    | Other databases and messaging                                       | Done    |
| 4c    | NTP, SNMP, SMTP, SIP, RADIUS and Steam                              | Done    |
| 4d    | Docker, system and remote browser monitors                          | Done    |
| 4e    | HTTP options, proxies and domain expiry                             | Done    |
| 4f    | Globalping, GameDig and Tailscale ping                              | Next    |
| 5     | Accounts and administration                                         | Planned |
| 6     | Integrations                                                        | Planned |

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

## Phase 2: Push, Ping and DNS (done)

- **Push:** `/api/push/:token` with `status`, `msg` and `ping`, by any method, with the same answers as Node.js. A late
  beat is DOWN, and an explicit `status=down` skips retries. Tokens are unique, made when left empty, and kept on edit.
- **Ping:** unprivileged ICMP sockets in Go, so the image stays `scratch` and non-root, without `NET_RAW`. The host
  must allow it with `net.ipv4.ping_group_range`, which `compose.yaml` and the Helm chart set for the container.
- **DNS:** all ten record types, several resolvers by IP address or hostname, and the last result on the monitor page.
- All three in the REST API, the web UI's form, and `kuma-import`, which keeps push tokens so senders keep working.

Tested with the race detector against TimescaleDB and MariaDB, and by a scripted client against the built image, with
real lookups and pings over the internet, on IPv4 and IPv6 loopback. Ping was tested as user 65534 with every
capability dropped, with and without the sysctl.

Not tested: the edit form in a browser, IPv6 over the internet, and installing on Kubernetes, so the chart's sysctl
has only been rendered and linted.

Known limits and differences from Node.js:

- Pushes are limited to 10 a second per monitor, with bursts of 20. Node.js had no limit.
- After a restart, an edit or maintenance, a push monitor's wait starts again, rather than counting from the last beat.
- DNS conditions aren't supported, so a DNS monitor with them is refused, and skipped by the importer.
- Ping's packet size is the data size, as with the `ping` command, but the numeric output setting is ignored.
- Records come in the order the resolver sends them, so a round-robin answer changes the message between checks.

## Phase 3: Status pages and badges (done)

- **Status pages:** made, edited and deleted in the UI, with groups, logos, themes, custom CSS, footers, analytics,
  tags and certificate expiry. The page's HTML is rendered with its data, as in Node.js.
- **Incidents:** posting, editing, resolving, unpinning and deleting, and the paged history.
- **RSS feed**, **custom domains**, the **entry page** setting, and the manifest for installing a page as an app.
- **Maintenance windows on status pages:** the status page options in the maintenance form now work.
- **Badges:** status, uptime, ping, average response, response and certificate expiry for monitors, and the overall
  state of a page. A port of badge-maker draws the same SVG, which tests compare with badge-maker's own output.
- The Helm chart's ingress can list the custom domains, in `ingress.statusPageHosts`.

Tested with the race detector against TimescaleDB, including a test that drives the UI's realtime events and the public
routes on a real database. Upstream's Playwright specs for status pages, incident history, public pages and badges
were ported and pass in headless Chromium against the built image: `internal/e2e/browser/status-pages.mjs`.

Not tested: custom domains through a real proxy or ingress, and installing on Kubernetes. Upstream's maintenance spec
passes only with the browser in the server's timezone, the Phase 1 limit, which Node.js has too.

Known limits and differences from Node.js:

- The REST API has no routes for status pages or incidents, and `kuma-import` doesn't copy them yet (Phase 5).
- Public answers are kept for up to a minute, rather than 5. Browsers are told to ask again each time, which fixes
  edits not showing until the cache expired.
- A monitor's address on a page leaves out any user name and password in it.
- Logos are stored in the database, rather than in the data directory, and must be up to 2 MB.
- A logo must be a PNG, or an http(s) address or a path; Node.js took any text.
- Matomo's address is used without its scheme, which makes it work: Node.js made `//https://...`.
- A ping badge with no beats is N/A, where Node.js showed `NaNms`. The status and response badges of a monitor without
  beats are N/A, where Node.js answered an error.
- Custom domains must be plain host names, and are lower-cased.
- Fixed in the UI: a public page showed "?" as its overall status, and grey status pills, because an upstream change
  had made it stop recording each monitor's newest beat.

## Phase 4: Remaining monitor types

Phase 4 was split, because each monitor type is its own small project with its own library and test target. The order is a
guess at what's used most; the dry run of `kuma-import` against the real database should decide it.

### Phase 4a: Conditions, JSON query, manual, WebSocket, PostgreSQL, MySQL, Redis (done)

- **Conditions:** the engine of Node.js's `server/monitor-conditions`, with groups, and/or and every operator. DNS and
  MySQL monitors use it, as in Node.js. DNS monitors with conditions, which Phase 2 refused, now work.
- **JSON query:** JSONata on the response of an HTTP request, compared with the expected value.
- **Manual**, **WebSocket upgrade**, **PostgreSQL**, **MySQL/MariaDB** and **Redis**.
- All of them in the REST API, the UI's form, and `kuma-import`.
- Fixed in the UI: switching a new monitor to WebSocket kept HTTP's accepted status codes, so it was DOWN on a normal
  close, because the form compared a code with a list of codes.

Tested with the race detector against TimescaleDB, MariaDB and Redis. JSON query results were compared with Node.js's
own `evaluateJsonQuery` running jsonata 2.2.2, for 37 queries. In headless Chromium against the built image, a monitor of
each type was made through the form and checked against a real PostgreSQL, MariaDB, Redis, a public WebSocket echo server
and DNS: `internal/e2e/browser/monitor-types.mjs`. The status page specs still pass.

Not tested: TLS to the databases, and Redis Sentinel or Cluster URLs, which aren't supported.

Known limits and differences from Node.js:

- JSONata is version 1.5, where Node.js had 2.2. Functions added since then fail as unknown.
- In JSON query errors, objects list their keys in order of name, and a `null` field shows as `undefined`.
- A MySQL value is compared as the driver's text: a date is `2026-11-05 00:00:00`, where Node.js had JavaScript's
  `Thu Nov 05 2026 …`.
- PostgreSQL's `ssl=true` means verified TLS, and other settings are libpq's, as pgx reads them.
- OAuth and mTLS on WebSocket monitors are refused, as they are on HTTP monitors.
- A CAA record without an `issue` tag is left out of DNS conditions, where Node.js's check failed with an error.

### Phase 4b: Other databases and messaging (done)

- **SQL Server**, **Oracle** and **MongoDB**, with conditions on SQL Server and Oracle, as in Node.js.
- **MQTT**, with the keyword, JSON query and conditions checks, over TCP, TLS and WebSockets.
- **RabbitMQ**, through each node's management API, and **Kafka producer**, with plain and SCRAM SASL logins.
- All of them in the REST API, the UI's form, and `kuma-import`.
- The settings of these types, and of the types still to come, are stored together in one JSON column, `settings`, so
  that a new type needs no new column.

Tested with the race detector against real SQL Server 2022, Oracle Database Free, MongoDB 7, Mosquitto, Kafka 3.9 and
RabbitMQ 3, and against a fake RabbitMQ API for the cases with alarms. In headless Chromium against the built image, a
monitor of each type was made through the form, checked against those servers, and opened again:
`internal/e2e/browser/monitor-types-4b.mjs`.

Not tested: TLS to SQL Server, Oracle, MongoDB, MQTT and Kafka, MQTT over WebSockets, SASL logins, and Oracle's
descriptor strings against a server.

Known limits and differences from Node.js:

- Kafka's AWS IAM login is refused, and so skipped by the importer.
- A value compared by conditions is the driver's text, so dates and decimals may read differently from Node.js.
- An Oracle statement that isn't a query is told apart by its first word, to report the rows it changed.
- MQTT waits for a message until the check's timeout, where Node.js waited 80% of the interval. With no timeout set,
  that's the same.

### Phase 4c: NTP, SNMP, SMTP, SIP, RADIUS and Steam (done)

- **NTP**, **SNMP** (gosnmp), **SMTP**, **SIP options**, **RADIUS** (layeh.com/radius) and **Steam**, as in Node.js.
- SIP options no longer runs `sipsak`: the `OPTIONS` request is made directly, so the image needs nothing installed.
- RADIUS requests carry a Message-Authenticator. FreeRADIUS 3.2.5 and later, patched for BlastRADIUS (CVE-2024-3596),
  drop requests without one, which the Node.js client didn't send.
- All of them in the REST API, the UI's form, and `kuma-import`.

Tested with the race detector against fake NTP, SIP, RADIUS, SMTP and Steam servers, and against a real Net-SNMP agent
(v1 and v2c) and FreeRADIUS. In headless Chromium against the built image, a monitor of each type but SIP was made through
the form and checked against pool.ntp.org, the SNMP agent, smtp.gmail.com with STARTTLS, FreeRADIUS, and Steam without a
key: `internal/e2e/browser/monitor-types-4c.mjs`.

Not tested: SNMPv3, SIP against a real SIP server, SMTPS, and Steam with a real API key.

Known limits and differences from Node.js:

- SIP is UDP only, as `sipsak` was by default. Any final answer but `200 OK` is DOWN, with the status line as the message,
  where Node.js failed with a generic error.
- SNMPv3 has no authentication or privacy, as in Node.js.
- Steam's ping is the API's response time, where Node.js pinged the game server.

### Phase 4d: Docker, system and remote browser monitors (done)

- **Docker container** monitors, with Docker hosts over a socket or TCP and TLS, managed under Settings > Docker Hosts.
- **System service** monitors ask systemd over D-Bus instead of running `systemctl`, so that they also work in a
  container that has the host's system bus mounted. **PM2** monitors run `pm2 jlist`, as in Node.js.
- **SFTP** (golang.org/x/crypto/ssh and pkg/sftp) and **gRPC keyword** monitors. The `.proto` text is compiled with
  bufbuild/protocompile, and the response is compared as protobuf JSON, with the field names Node.js gave.
- **Real browser** monitors use the Chrome DevTools Protocol (chromedp), with remote browsers managed under
  Settings > Remote Browsers. Screenshots are kept in the database and served to logged-in users only.
- All of them in the REST API, the UI's form, and `kuma-import`, which also copies Docker hosts and remote browsers.
- The form no longer hides system service and SIP options monitors when the server runs in a container.

Tested with the race detector against a fake Docker API and the real daemon, an in-process gRPC health server and SFTP
server, this machine's systemd, a faked `pm2`, and headless Chromium both started locally and reached through its
DevTools port. In headless Chromium against the built image, with the Docker socket and the system bus mounted, a
Docker host and a remote browser were added and tested in the settings, and a monitor of each type was made through
the form, checked, and opened again, against a TimescaleDB container's health check, the `docker` service, a missing
`pm2`, atmoz/sftp, a gRPC health server, and example.com through the remote browser, whose screenshot the monitor's
page showed: `internal/e2e/browser/monitor-types-4d.mjs`.

Not tested: Docker hosts over TCP or TLS, PM2 with real PM2, SFTP against a server that only takes
keyboard-interactive logins, gRPC with TLS or metadata, and remote browsers such as browserless.

Known limits and differences from Node.js:

- Remote browsers must speak the Chrome DevTools Protocol. Node.js connected with Playwright's own protocol, so a
  Playwright endpoint, such as browserless's `/playwright/chromium`, needs changing to its CDP address.
- The image has no Chrome, so a real browser monitor needs a remote browser, or the server running next to Chrome.
  Node.js installed Chromium in its container when one was first needed.
- In a container, system service monitors need the host's system bus and an AppArmor profile that allows D-Bus, such
  as `--security-opt apparmor=unconfined`. Windows services aren't supported.
- PM2 monitors don't work in the image, which has no `pm2`.
- A real browser check waits for the page's load event, where Node.js waited for `DOMContentLoaded`. Its ping is the
  time to the response's headers.
- A gRPC keyword that isn't found says `keyword [x] is not in [response]`, without the stray quotes and plus signs of
  the Node.js message. The metadata, which Node.js ignored, is sent.
- A Docker check's ping is the API's response time, where Node.js set none.

### Phase 4e: HTTP options, proxies and domain expiry (done)

- Every HTTP option of the form: descriptions, cache busting, IP family, saved responses for notification templates,
  and retrying a JSON query monitor only when the status fails.
- NTLM (Azure/go-ntlmssp), mTLS and OAuth2 client credentials logins. Basic and bearer logins are now kept as the form
  set them, where before they became an `Authorization` header; monitors saved that way still work.
- Proxies over HTTP, HTTPS, SOCKS5 and SOCKS4, managed under Settings > Proxies.
- Domain expiry tracking over RDAP, with alerts on the days set in the settings, the expiry date on the monitor's page,
  and the form's explanation of a domain that can't be tracked.
- `kuma-import` copies proxies and every one of these options, so it no longer skips monitors with a proxy or with an
  NTLM, mTLS or OAuth login, and reports no dropped settings.

Tested with the race detector against local servers: an HTTP proxy and SOCKS5 and SOCKS4 servers with logins, a server
that requires a client certificate, an OAuth2 token endpoint that checks the client's credentials and revokes a token,
an NTLM server that answers the first handshake message, and a fake RDAP server and IANA list, including renewals. In
headless Chromium against the built image, a proxy with a login was added in the settings, and a keyword monitor of
https://example.com made through the form with the proxy, cache busting, IPv4, a description and domain expiry. Its
check went through the proxy, editing showed the options, its page showed the description and the expiry date from
Verisign's RDAP server, and the form explained why 10.0.0.1 can't be tracked: `internal/e2e/browser/monitor-options-4e.mjs`.

Not tested: NTLM against a real Windows server past the first handshake message, OAuth2 against a real identity
provider, HTTPS proxies, mTLS on WebSocket monitors, and domain expiry alerts reaching a real notification.

Known limits and differences from Node.js:

- The NTLM workstation is stored but not sent, which go-ntlmssp doesn't support.
- Saved responses aren't stored with the beat, only given to its notifications, which is what the form says they're for.
- A proxy's cookies aren't kept between checks, as Node.js's proxy agents did.
- A monitor whose proxy no longer exists fails, where Node.js connected directly. Deleting a proxy takes it out of its
  monitors, so this only happens if the database is changed by hand.
- A proxy's "active" switch is honoured. Node.js saved every proxy as active, whatever the form said.

### Phase 4f: Globalping, GameDig and Tailscale ping (planned)

These need more than a port of the check, so they come last:

- **Globalping** is a client of an external measurement API, with ping, HTTP and DNS sub-types.
- **GameDig** wraps a JavaScript library that speaks the protocols of over 300 games. A Go port could cover the common
  protocols, such as Valve's A2S and Minecraft's, if they're in use.
- **Tailscale ping** runs the `tailscale` command, which needs `tailscaled` in the container.

## Phase 5: Accounts and administration (planned)

- Two-factor login, more than one account, the leaked password check, and API keys in the UI
- Settings that take effect, such as the data retention setting, which `RETENTION_DAYS` decides today
- The Monitor History page: clearing heartbeats and statistics, shrinking the database, and the database size
- Importing users, status pages and maintenance windows from MariaDB

## Phase 6: Integrations (planned)

- The MCP server, the Cloudflare tunnel and backup upload
- The update check
- The `nostr` and `Webpush` notification providers, and `apprise` in the image
