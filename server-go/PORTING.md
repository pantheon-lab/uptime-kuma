# Porting Uptime Kuma to Go

This records what has moved from the removed Node.js server to this service, phase by phase, and what's left. The
reference for behaviour is the Node.js server in git, at `921745bf^`.

Update this file in the same pull request as each phase. Only mark a phase done when it has been tested, and say what
wasn't. The order of the planned phases is a proposal, and should follow what's actually in use: the dry run of
`kuma-import` lists every monitor it can't bring over, and why.

| Phase | Scope                                                               | Status |
| ----- | ------------------------------------------------------------------- | ------ |
| 0     | Core: checks, notifications, history, web UI, login, deployment     | Done   |
| 1     | Maintenance windows and public holidays, tags, TLS certificates     | Done   |
| 2     | Push, Ping and DNS monitors                                         | Done   |
| 3     | Status pages and badges                                             | Done   |
| 4a    | Conditions, JSON query, manual, WebSocket, PostgreSQL, MySQL, Redis | Done   |
| 4b    | Other databases and messaging                                       | Done   |
| 4c    | NTP, SNMP, SMTP, SIP, RADIUS and Steam                              | Done   |
| 4d    | Docker, system and remote browser monitors                          | Done   |
| 4e    | HTTP options, proxies and domain expiry                             | Done   |
| 4f    | Globalping, GameDig and Tailscale ping                              | Done   |
| 5a    | Two-factor login, leaked passwords, API keys in the UI              | Done   |
| 5b    | Settings that take effect, and the Monitor History page             | Done   |
| 5c    | Importing accounts, status pages and maintenance windows            | Done   |
| 6a    | The MCP server and the update check                                 | Done   |
| 6b    | The `nostr` and `Webpush` notification providers                    | Done   |
| 6c    | `apprise` in the image, and the Cloudflare tunnel                   | Next   |

## Phase 0: Core (done)

- HTTP, keyword, TCP port and group monitors, with retries and upside-down mode
- Notifications. `apprise` is ported, but its command isn't in the image. `nostr` and `Webpush` came in Phase 6b
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

- The REST API has no routes for status pages or incidents. `kuma-import` copies them since Phase 5c.
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

### Phase 4f: Globalping, GameDig and Tailscale ping (done)

With these, every monitor type of the Node.js server is ported.

- **Globalping**: ping, HTTP and DNS measurements through the Globalping API, with the token from the settings.
- **GameDig**: the protocols of 273 of gamedig's 358 games, written in Go: Valve's A2S, GameSpy 1, 2 and 3, Quake 2 and
  3, All-Seeing Eye, and Minecraft over Java, Bedrock and GameSpy 3. The form lists only those games, from a list that
  `internal/check/gamedig-games.mjs` makes from gamedig's.
- **Tailscale ping** asks tailscaled's local API over its socket instead of running `tailscale`, so the image needs
  nothing installed, and the form now offers it in a container.
- All of them in the REST API, the UI's form, and `kuma-import`.

Tested with the race detector against fake servers of each GameDig protocol, including Valve's challenge, GameSpy 1's
and 3's answers in parts, and GameSpy 3's challenge, a fake tailscaled and Globalping API, and for real: a Minecraft
26.3 server, a ping through this machine's tailscaled to a peer, and a ping from the Globalping API. In headless
Chromium against the built image, with tailscaled's socket mounted, a monitor of each type was made through the form,
checked against those, and opened again: `internal/e2e/browser/monitor-types-4f.mjs`.

Not tested: the GameDig protocols other than Minecraft against real game servers, Globalping's HTTP and DNS
measurements against the real API, and Globalping with a token.

Known limits and differences from Node.js:

- The other 85 GameDig games, such as Battlefield's, FiveM's and TeamSpeak's, which have their own protocols, can't be
  added. A monitor of one fails, and the importer skips it. Valve servers that split an info answer into several packets
  aren't supported, which is rare.
- GameDig's ping is the time to the first answer, and a Minecraft monitor's the time to connect, as in gamedig.
- A Globalping HTTP monitor's certificate isn't kept or alerted on, though an unauthorized one is still DOWN. Its JSON
  query reads the response's body, where Node.js read the whole output, headers too, which only a body could pass.
- Tailscale ping messages say what failed in words of their own, as before, without the command's output.

## Phase 5: Accounts and administration

Phase 5 was split like Phase 4. More than one account, which the plan listed, was left out: the Node.js server
didn't have it either. better-auth's admin routes for it were turned off there, and the UI has no page for
accounts, so there's nothing to port. It needs a design of its own first, such as what each account may do.

### Phase 5a: Two-factor login, leaked passwords and API keys (done)

- **Two-factor login** with an authenticator app, through the same better-auth routes the UI calls:
  `/two-factor/enable`, `/two-factor/verify-totp` and `/two-factor/disable`, and a login that answers
  `twoFactorRedirect` when a code is needed. Codes are RFC 6238, as better-auth's.
- **The leaked password check** against Pwned Passwords, at setup and when the password changes, as in Node.js.
  `KUMA_DISABLE_LEAK_PASSWORD_CHECK` turns it off, as `UPTIME_KUMA_DISABLE_LEAK_PWD_CHECK` did.
- **API keys in the UI:** Settings > API Keys lists, adds and deletes the REST API's keys. The dialog now picks
  the key's scopes, and the list shows them, the key's prefix and when it was last used.
- `KUMA_INITIAL_ADMIN_USERNAME` and `KUMA_INITIAL_ADMIN_PASSWORD` create the account at start, as
  `UK_INITIAL_ADMIN_USERNAME` and `UK_INITIAL_ADMIN_PASSWORD` did.

Tested with the race detector: the routes against an in-memory store, with RFC 6238's test vectors, replayed
codes, the limits on tries, and a fake Pwned Passwords API that pads its answers, and the store against TimescaleDB,
including concurrent uses of one code. In headless
Chromium against the built image, against the real Pwned Passwords API: setup refused `password123`, two-factor
login was turned on through Settings > Security with a code computed from the QR code's key, a login asked for a
code, refused a wrong one and took a right one, a key with only `monitors:read` was added through the form, could
read monitors and not add one, and stopped working when deleted, and two-factor login was turned off:
`internal/e2e/browser/accounts-5a.mjs`. The initial account variables were tried against the image.

Not tested: a real authenticator app scanning the QR code, and a server that can't reach Pwned Passwords
other than through a fake that fails.

Known limits and differences from Node.js:

- There are no backup codes. better-auth made them, but the UI never showed them. Losing the app means turning
  two-factor login off in the database, which the README explains.
- A code can't be used twice, even within its 30 seconds, where better-auth allowed it. Turning two-factor login
  on ends the account's other logins, and better-auth's "trust this device" isn't offered, as the UI never asked.
- The authenticator key is stored as it is, where better-auth encrypted it with its secret, which here would sit
  in the same configuration as the database's password.
- Deleting an API key revokes it, which the REST API's list still shows, and the UI's doesn't. A key made in the
  UI without choosing scopes, by an older page, gets them all.
- Keys made by the Node.js server, by better-auth or in its older table, aren't imported, and Prometheus's
  `/metrics`, which they unlocked, is replaced by OpenTelemetry.

### Phase 5b: Settings and the Monitor History page (done)

- **Retention** comes from Settings > Monitor History, as `keepDataPeriodDays` did in Node.js, and saving it changes
  TimescaleDB's retention policy straight away. `RETENTION_DAYS` is only its first value: at start, a setting that
  was never saved, or isn't valid, is saved from it, so that the page shows the days that apply, and saving any
  settings page doesn't put the UI's default in their place. When both are set and differ, the start-up log says
  that `RETENTION_DAYS` is ignored.
- **Clearing data:** `clearHeartbeats` for one monitor and `clearStatistics` for all of them delete the beats, the
  hourly and daily statistics, including those older than the beats that retention kept, and the statistics
  imported from Node.js. The monitors are stopped first, so that queued beats aren't written after, and started
  again, so that they start a new history as Node.js's restart did.
- **The database size** (`getDatabaseSize`), now shown for PostgreSQL too.
- **Search Engine Visibility** answers `/robots.txt`, as Node.js did.
- Settings that can't take effect are gone from Settings > General: the DNS cache (nscd), Prometheus and "API and
  MCP" switches. The server timezone is shown, but can't be changed there, as it comes from `KUMA_TIMEZONE`.

The other settings already took effect: the entry page, the primary base URL, trusting a proxy, the Steam key, the
Globalping token, the Chrome executable, and the certificate and domain expiry days.

**A fix for data loss:** rebuilding the aggregates, which deleting a monitor does, refreshed them from the
beginning of time, which recomputes each bucket from beats. Once retention had dropped beats, that deleted every
monitor's hourly and daily statistics from before the beats kept. The hourly aggregate is now refreshed only from
the oldest chunk of beats kept. The daily one is built on the hourly one, which keeps everything, so it's refreshed
as before.

Tested with the race detector: the events against an in-memory store, and the store against TimescaleDB, where two
monitors had 40 days of beats and retention dropped all but 10 of them. Clearing one left the other's 40 days of
statistics, and so did deleting a monitor afterwards, which lost 30 of them before the fix. Clearing everything left
nothing, and new beats counted afterwards. In headless Chromium against the built image
(`internal/e2e/browser/history-5b.mjs`), 7 of 7 steps passed with no page errors:

- the page showed the 365 days that applied, and the database size, and no Shrink Database button
- saving 30, 0 and 45 days changed the policy each time
- a monitor with 20 days of statistics lost them all through Clear Data > Heartbeats, and kept checking
- Clear all Statistics left no beats or statistics, and kept the retention
- `/robots.txt` followed Search Engine Visibility
- Settings > General no longer showed the removed switches

Restarting the image with `RETENTION_DAYS=10` kept the saved 45 days and logged why, and on a new database it saved 10.

Not tested: a database large enough for clearing to take long. Clearing stops the monitors one at a time, and
runs within one realtime event, so other events from that browser wait until it's done, up to 5 minutes.

Known limits and differences from Node.js:

- There's no Shrink Database. PostgreSQL's autovacuum does what a `VACUUM` would, and dropped or cleared beats free
  their space at once. Node.js offered it for SQLite only.
- Node.js deleted daily statistics older than the retention too. Here they're kept, as they were before this phase,
  so that uptime over longer periods still works.
- Beats are dropped a whole day at a time, so up to a day more than the setting is kept.
- `apiEnabled` doesn't turn the REST API off, as existing keys would stop working. The keys guard it instead.

### Phase 5c: Importing the account, status pages and maintenance windows (done)

- **The account**, from better-auth's tables: the username, the scrypt password hash as it is, and the authenticator
  key, which better-auth encrypted with XChaCha20-Poly1305 under its secret. `SOURCE_AUTH_SECRET` gives that secret.
  Without it, an account with two-factor login on isn't imported, as the password alone would then log in. It's only
  imported when there's no account here. When nobody logged in since better-auth arrived, the account is still in
  the older `user` table, as Node.js moved it at the next login: its bcrypt hash is imported, and the first login
  here replaces it with scrypt.
- **Status pages** with their settings, groups and monitors, incidents, custom domains and uploaded logos, which
  are files in the Node.js data directory, read from `SOURCE_DATA_DIR`.
- **Maintenance windows** with their monitors and status pages. The dates, times of day, days and cron are
  converted to this service's fields. A window set to the server's timezone gets the Node.js server's timezone
  setting, so that it keeps its times.
- Each page and window is copied once, and remembered in `status_page_import` and `maintenance_import`, so that
  imports can run again without overwriting changes made here.

Tested with the race detector, against MariaDB and TimescaleDB. The source tables for these came from running the
knex init and migrations of the Node.js backend at `921745bf^` on MariaDB, and are in the importer's test schema.
The two-factor key in the tests was encrypted with better-auth's own `symmetricEncrypt`, and codes from it were
checked against better-auth's `createOTP`. The tests cover:

- an account refused without the secret or with a wrong one, imported with it, and not imported again
- the older table's account
- pages with an uploaded logo, a logo address, a missing logo file, a slug already used, and a domain already used
- monitors that weren't imported
- incidents
- each kind of window, and an invalid one
- links to monitors and pages
- running again, and copying a page again after deleting it

End to end (`internal/e2e/browser/import-5c.mjs`), a source database was created by those migrations and filled
with an account made by better-auth's own hashing and encryption, a status page with a logo, an incident and a
window. `kuma-import` from the built image refused the account without `SOURCE_AUTH_SECRET` and imported it with
it. Then in headless Chromium, 4 of 4 steps passed with no page errors: setup wasn't offered, the login took the
password and a code from the imported key, the window kept its timezone, and the status page showed the logo,
incident, monitor and footer.

Not tested: a database from a real Node.js server, as opposed to one its migrations made and the tests filled.

Known limits and differences from Node.js:

- Only the first account is imported. Node.js used only one.
- Backup codes aren't imported, as this service has none.
- Changes made in Node.js to a page or window after it was copied aren't picked up. Delete it here and import again.
- A page's `published`, `password` and `search_engine_index` columns aren't imported. Node.js didn't use them.
- Other settings, such as `primaryBaseURL`, still aren't imported.

## Phase 6: Integrations

Phase 6 was split like the others. "Backup upload", which the plan listed, isn't ported: the Node.js server at
`921745bf^` had no backup feature, only its old JSON backup, removed before.

### Phase 6a: The MCP server and the update check (done)

- **MCP** at `/mcp`, over Streamable HTTP, stateless, as in Node.js. Its tools are the REST API's read-only
  operations, built from `openapi.json`, as Node.js built them from its routes. Node.js left out routes with a path
  parameter; here they're tools too, with an input schema from the document, so `getMonitor` and `getBeats` work.
  Each call is made to the REST API in-process with the caller's key, so scopes and rate limits apply as on the
  API. It uses the official Go SDK, `github.com/modelcontextprotocol/go-sdk`.
- **The update check** reads this fork's `v*` tags from GitHub every 48 hours, as Node.js asked uptime.kuma.pet,
  whose versions are upstream's. "Show update if available" and "Also check beta release" work as before, and
  changing them checks again. The UI's links go to this fork's tags.

Tested with the race detector: the MCP server through the SDK's own client against the real `openapi.json`, with
the tools, their arguments, a key without the scope, and no key; the update check against a fake list of tags,
with pre-releases, the settings and rechecks. Against the built image (`internal/e2e/browser/integrations-6a.mjs`),
6 of 6 steps passed with no page errors:

- `/mcp` over plain HTTP, as a client sends it: refused without a key; with a `monitors:read` key it
  initialized, listed 11 tools, answered `listMonitors` with the monitor, and refused `getBeats` with the API's 403
- the update notice in headless Chromium, with tags served by the script: 3.0.0 with the notice, 3.1.0-beta.1 with
  betas, and nothing with checking off

The default address was also tried against GitHub, where the fork has no tags yet, so there's no notice.

Not tested: an AI assistant's MCP client, other than the SDK's and the plain HTTP above.

Known limits and differences from Node.js:

- MCP tools can't change anything, as in Node.js. Tools are listed whatever the key's scopes, and a call without
  the scope answers with the API's error.
- An MCP request counts against the key's rate limit, and so does each tool call it makes.
- The update notice links to this fork's tags, not upstream's releases.

### Phase 6b: The nostr and Webpush notification providers (done)

- **Webpush:** RFC 8291 encryption and VAPID with `github.com/SherClockHolmes/webpush-go`, with web-push's four-week
  TTL, as Node.js sent. The VAPID key pair is made the first time the form asks for the public key
  (`getWebpushVapidPublicKey`), once even when two pages ask at once. The private key is no longer sent to the
  browser with the other settings, and the settings page can't replace either key. `kuma-import` copies Node.js's
  pair, unless there's one here, so that subscribed browsers keep receiving.
- **nostr:** a NIP-17 private message to each recipient, gift-wrapped with NIP-59, using
  `github.com/nbd-wtf/go-nostr` for keys, NIP-44 encryption and the wrap. Relays are written to with our own small
  NIP-01 client on `coder/websocket`, which logs in with NIP-42 when a relay asks, because go-nostr's relay client
  has a data race between closing and its read loop. It succeeds when any relay takes the messages.

Tested with the race detector:

- Webpush against a fake push service that decrypts the message as a browser does and verifies the VAPID token,
  plus an ended subscription and a missing one
- nostr against a fake relay that requires NIP-42, unwrapping each message as its recipient, with a relay down
- the key pair made by racing requests, kept by an import, and copied by `kuma-import`
- the private key left out of the settings

nostr was also tested against real relay software, `scsibug/nostr-rs-relay` in Docker (`TEST_NOSTR_RELAY`). Against
the built image (`internal/e2e/browser/notify-6b.mjs`), 4 of 4 steps passed with no page errors: a nostr
notification set up in the form and tested, then read from that relay with nostr-tools 2.20, the library Node.js
used, which unwrapped it as the recipient; and the Webpush form's public key, the same each time, with the private
key absent from the settings.

Not tested: a real browser receiving a Webpush message, which headless Chromium can't subscribe for, and public
nostr relays.

Known limits and differences from Node.js:

- A relay that can't be reached no longer fails the whole notification, as it did in Node.js, whose connect wasn't
  caught: the others are still tried, and the error names each relay that failed.
- The VAPID subject is this fork's repository rather than upstream's.

### Phase 6c: apprise in the image, and the Cloudflare tunnel (next)

Both add programs to an image that is now `FROM scratch`: `apprise` is Python, and the tunnel runs `cloudflared`.
