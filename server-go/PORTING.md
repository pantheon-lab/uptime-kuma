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
| 6c    | `apprise` in the image, and the Cloudflare tunnel                   | Done   |
| 7a    | More than one account, with full and read-only roles                | Done   |
| 7b    | Logging in with GitHub                                              | Done   |
| E1    | Managing accounts in the UI                                         | Done   |
| E2    | The Security page for GitHub accounts                               | Done   |
| E3    | Encrypting stored secrets                                           | Done   |
| E4    | Completing the migration from MariaDB                               | Done   |
| U1    | Tab titles, unsaved edits, clickable stats, this fork's help links  | Done   |
| U2    | Filtering events, and grouping alerts in the UI                     | Done   |
| U3    | Keyboard shortcuts and a command palette                            | Done   |
| UA    | A prompt for an AI agent to trace a failing monitor                 | Done   |
| U4    | The Edit Monitor form                                               | Done   |
| U5    | The monitor's page                                                  | Done   |
| U6    | Editing many monitors at once                                       | Done   |
| U7    | Viewers, the first run and phones                                   | Done   |
| U8    | Bootstrap 5.3 and accessibility                                     | Done   |
| U9    | Status page incidents and subscriptions                             | Done   |
| S1    | Managing single sign-on in the UI                                   | Done   |
| S2    | Logging in with Google                                              | Done   |
| S3    | Logging in with OpenID Connect                                      | Done   |

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

### Phase 6c: apprise in an image, and the Cloudflare tunnel (done)

The image is `FROM scratch`, with only the two programs, so both were decided with the team:

- **apprise** is in a second image, the Dockerfile's `apprise` target on `python:3.14-alpine3.24` with apprise
  2.1.0, published with an `-apprise` suffix on every tag, about 120 MB larger. The chart's `image.apprise` picks it.
  The default image is as before, and a test there says the command isn't installed.
- **The Cloudflare tunnel** runs Cloudflare's `cloudflared` beside the service, rather than the service starting it
  as Node.js did: a `tunnel` profile in `compose.yaml`, and `cloudflared.enabled` in the Helm chart, with the token in
  a secret. Settings > Reverse Proxy explains this instead of its start and stop buttons, and the UI no longer asks
  for `cloudflared_*` events.

Tested: both targets built. In the apprise image, `apprise` 2.1.0 runs as `nobody`. Against both images
(`internal/e2e/browser/apprise-tunnel-6c.mjs`), 2 of 2 steps passed on each, with no page errors:

- an apprise notification tested through the REST API: from the apprise image, it reached a listener through
  `json://`; from the default image, it reported that the command isn't installed
- the Reverse Proxy page showed the instructions, and its Trust Proxy setting still saved

`compose.yaml` was checked with and without the profile, and the chart was linted and rendered with the tunnel, with
an existing secret, without a token (refused), and with `image.apprise`. The release workflow builds both targets in
a matrix.

Not tested: a real Cloudflare Tunnel, which needs a Cloudflare account, and the release workflow itself, which runs
when this reaches `main`.

Known limits and differences from Node.js:

- The tunnel can't be started or stopped from the UI.
- Other apprise versions need a rebuilt image.

## Phase 7: Logging in with GitHub

Not in the Node.js server, which only had a password and an authenticator app. Pantheon Lab's team signs in to
GitHub already, so this lets them log in to Uptime Kuma the same way, and lets access follow GitHub: leaving the
organization ends it.

Decided with the team:

- **Roles:** members of some GitHub teams get full access, and members of others can only look. The password
  account keeps full access.
- **Two-factor login** for GitHub accounts is left to GitHub, where the organization can require it.
- **An OAuth App**, with the `read:org` scope to read team membership.

It's split in two, because read-only access needs more than one account first, and more than hiding buttons: every
logged-in page is sent monitors with their credentials, notification settings and proxy passwords.

### Phase 7a: More than one account, with full and read-only roles (done)

- Accounts have a role, `admin` or `viewer`. Existing accounts and the one made at setup are admins.
- The server enforces it. A viewer's realtime events are limited to a list of those that only read, so that an
  event added later is closed to viewers until it's added there; anything else answers "Your account can only
  view".
- A viewer gets no credentials. Monitors are sent as a list of the fields the pages show, again so that a field
  added later stays hidden, with URLs stripped of their credentials, query and fragment. The lists of
  notifications, proxies, Docker hosts and remote browsers come empty, and broadcasts of them go to admins only.
  A monitor's page loads it with `getMonitor`, which a viewer gets in the same form.
- The session carries the role (`session` event, and better-auth's `user.role`). The UI hides the buttons that
  change things, the settings menu and the monitor list's selection, and sends a viewer from forms and settings
  back to the dashboard.

Tested with the race detector: the role in the store and in sessions, and a viewer's stream over HTTP, which got the
monitor without six kinds of secret that an admin's list has, empty lists, reads that work, eleven kinds of change
refused, and broadcasts without credentials. In headless Chromium against the built image
(`internal/e2e/browser/roles-7a.mjs`), 6 of 6 steps passed with no page errors: the admin's page held the
monitor's credentials and the viewer's held none; the viewer's monitor page rendered without the edit, delete and
clear buttons, and the dashboard, maintenance and status page lists without their add buttons; settings and forms
sent the viewer back; and the server refused a delete sent directly.

Known limits:

- There's no page for managing accounts. Phase 7b makes them from GitHub logins; until then a viewer is made in the
  database.
- A viewer still sees heartbeat messages, which can quote a response, and status pages' settings, which are public.

### Phase 7b: Logging in with GitHub (done)

- "Log in with GitHub" on the login page, with an OAuth App: the authorization code flow with `state` and PKCE
  (S256), both kept in a cookie only the flow's paths receive, for 10 minutes. The `read:org` scope reads
  membership: of the organization, then of its teams.
- `KUMA_GITHUB_ADMIN_TEAMS` and `KUMA_GITHUB_VIEWER_TEAMS` decide the role, at each login and once a day while a
  session is used. Someone no longer in the teams is logged out everywhere, and a changed role takes effect, with
  open pages reconnecting. When GitHub can't be reached, the account keeps its role until the next try. Turning
  GitHub login off ends its accounts' sessions.
- An account per GitHub user, by GitHub id, named `@login`. A renamed login whose old name someone else takes
  leaves the old account as `@name#id`. Password accounts can't start with `@`.
- `KUMA_PASSWORD_LOGIN=false` refuses the password, and the login page then shows only GitHub. The server won't
  start with it unless GitHub login is set up.
- A refused login comes back to the login page with a reason.

Tested with the race detector, against a fake GitHub that checks the client secret and the PKCE verifier:

- admin and viewer teams, and a team of the same name in another organization, which counts for nothing
- refusals: no team, not a member of the organization, a forged state, no flow cookie, a cancelled login, and a
  bad code
- the next day: a move to a viewer team making the session a viewer, leaving the organization ending the session,
  and GitHub being down keeping it
- password login off
- accounts in the store, with renames, rechecks and ending sessions

In headless Chromium against the built image (`internal/e2e/browser/github-7b.mjs`), with a fake GitHub that the
script serves and that redirects back as GitHub does, 7 of 7 steps passed with no page errors:

- the login page offering both, and the password account still logging in
- an admin team's member logging in as `@alice`, an admin
- a viewer team's member as a viewer, without the monitor's token
- someone in no team refused with the reason shown
- with `KUMA_PASSWORD_LOGIN=false`, only GitHub offered and the password refused

Not tested: the real GitHub, which needs an OAuth App registered for the organization.

Known limits:

- The GitHub token is stored as it is, as the authenticator key is, to check the teams again. It can read the
  user's organizations and teams, nothing else.
- Someone who leaves the teams is logged out at their next use, within a day, and can't log in again. E1 added
  listing and removing accounts.
- The Security page's password and two-factor settings didn't apply to GitHub accounts, which have neither. E2
  replaced them for those accounts.

## Enhancements (done)

The port is complete. These go beyond what the Node.js server did, and came out of Phases 5 to 7.

### E1: Managing accounts in the UI (done)

GitHub teams were the only control over accounts, and an account that had left them stayed in the database.

- Settings > Accounts, for admins: every account, with its role, whether it logs in with GitHub or a password, when
  it last logged in (a new column, set with each session), and its open sessions, marking the page's own.
- Ending an account's sessions (an admin's own page stays logged in), and removing an account. Removing a GitHub
  account only lasts until its next login while it's still in the teams, and the confirmation says so.
- Changing a password account's role, which makes its open pages reconnect with the new one. A GitHub account's role
  follows its teams, and the server refuses changing it.
- The last admin who can log in can't be removed or made a viewer. "Can log in" follows the configuration: a
  password admin needs password login on, a GitHub admin needs GitHub login on. The check holds a lock, so that two
  admins removing each other at once can't both succeed. Admins can't remove or demote themselves either.
- Logins were logged already; removals, role changes and ended sessions are logged with who made them.
- The events are admin-only, by not being in the viewer list.

Tested with the race detector: the store against TimescaleDB (the listing, the last-admin rule with and without
GitHub login, two concurrent removals of the only two admins, ending all sessions but one), and the events against
the fake store (every refusal, role changes, ending sessions, removing, and a viewer refused all four). In headless
Chromium against the built image (`internal/e2e/browser/accounts-e1.mjs`), 6 of 6 steps passed with no page errors:
the list with roles, methods, last logins and sessions; making a viewer an admin, which their open page picked up;
ending their sessions, which logged them out; removing a GitHub account with the warning shown; and the server
refusing self-removal, self-demotion and a viewer's request.

Known limits:

- Password accounts can't be created here: the setup account is the only one, and others come from GitHub.
- A GitHub admin that has left the teams still counts as an admin who can log in until its next check.

### E2: The Security page for GitHub accounts (done)

GitHub accounts have no password and leave two-factor login to GitHub, but the Security page offered both.

- The organization's teams an account is in are kept at each login and check (a new column), and a GitHub
  account's Security page shows its organization, teams, role and last check, and how often it's checked, instead
  of the password, two-factor and "disable auth" settings. The page asks with `getOwnAccount`.
- The server refuses changing the password, starting two-factor login and confirming its code for GitHub accounts
  (`GITHUB_ACCOUNT`).

Tested with the race detector: the teams stored at login (only the organization's), the three refusals, and
`getOwnAccount`, against the fake GitHub; the store's teams against TimescaleDB. In headless Chromium against the
built image (`internal/e2e/browser/security-e2.mjs`), with a fake GitHub, 4 of 4 steps passed with no page errors:
the GitHub account's page with its organization, teams, role and last check and without the password and
two-factor settings, the server refusing both changes, and a password account's page unchanged. Hiding "disable
auth" for GitHub accounts came after that run, and was only linted.

### E3: Encrypting stored secrets (done)

Authenticator keys, GitHub tokens, notification settings, monitor credentials and the VAPID private key were stored
as they are, protected only by the database's access control.

- `KUMA_SECRET_KEY` (or `KUMA_SECRET_KEY_FILE`), 32 bytes in base64 or hex, never in the database. Values are
  sealed with AES-256-GCM with the table, column and row as associated data, so that a value moved to another row
  doesn't open (`internal/secrets`). A new row's id is taken from its sequence first, so that it's sealed for it.
- Encrypted: authenticator keys, GitHub tokens, notification settings (the whole `config`), proxy passwords and the
  VAPID private key. **Decided against:** monitors' credentials, which are spread over many columns that checks, the
  API and the UI read, and which the Grafana role can't read already; and Docker hosts and remote browsers, whose
  addresses rarely hold credentials. Comparisons that the database made on encrypted columns (confirming the
  authenticator key, matching an imported proxy) are made after opening them.
- Each value carries its key's id, the start of the key's SHA-256. `KUMA_SECRET_KEY_OLD` holds keys being
  replaced, and `kuma-analytics rekey` encrypts everything again with the current key.
- At each start, plain values are encrypted when there's a key, and the log counts them. The server, and
  `kuma-import -apply`, refuse to start with encrypted values and no key, or a key that doesn't open them.

Tested with the race detector: the keyring (sealing, a moved value, no key, unknown keys, parsing keys), and against
TimescaleDB, each of the five kinds stored before there was a key, encrypted at start, read back, written anew
encrypted (with two-factor confirmation and proxy matching on encrypted values), a value copied to another row
refusing to open, refusing to start without the key, and rotation. The whole store suite also passes with
`TEST_SECRET_KEY` set, so that every stored secret in it is encrypted. Against the built image
(`internal/e2e/browser/secrets-e3.mjs`): a notification and a proxy saved without a key, then a start with one that
logged both encrypted, the database holding only `kenc:v1:` values, the notification dialog showing the webhook,
and a new notification stored encrypted; then a start without the key refused, `rekey` with a new key, a start
with the new key alone, and one with the old key alone refused.

Not tested against the image: authenticator keys, GitHub tokens and the VAPID key, which the store tests cover.

Known limits:

- Turning encryption off isn't supported: nothing decrypts the values back.
- Losing the key loses those values. The README says to back it up apart from the database.

### E4: Completing the migration from MariaDB (done)

`kuma-import` copied monitors, every raw beat, the hourly and daily statistics, notifications, tags, proxies, Docker
hosts, remote browsers, the account, status pages and maintenance windows. This finishes the move, so that nothing
has to be set up again by hand, and the move can be checked.

- **Settings:** `primaryBaseURL`, the entry page, search engine visibility, trusting a proxy, the certificate and
  domain expiry days, the retention, the Steam key, the Globalping token, the Chrome executable, and the update
  check's. `app_setting` now records where each value came from (`default`, `ui` or `import`): the import replaces
  only defaults and its own values, and the report lists each setting as copied, same, kept with why, or not in the
  source, never with its value. Values from before this version have no source, and are kept.
- **API keys:** better-auth's (`better_auth_apikey`), whose stored hash is the base64url of the key's SHA-256, the
  same digest this service stores, so they're looked up as its own keys are; and the older table's `uk<id>_<secret>`
  keys, with their bcrypt hash, which the REST API now checks (remembering a match, but looking the key up each
  time, so that revoking works at once). Names, creation and expiry dates and whether they're on come along; a key
  revoked here stays revoked. Uptime Kuma gave keys no permissions, so they get every scope. Keys without this
  service's prefix are only looked up when they're at least 32 characters.
- **Alert state:** certificate alerts sent (`notification_sent_history`) into `cert_alert_sent`, the certificates
  last seen (`monitor_tls_info`), unless one was seen here, and `domain_expiry`, with the last alert sent, unless
  checked here since.
- **Skipped monitors:** their beats and statistics are kept in `skipped_heartbeat` and `skipped_stat`, by source id,
  and a monitor imported later gets them (beats and statistics that aren't here already), even after Uptime Kuma
  pruned them.
- **`kuma-import -verify`** compares each imported monitor with the source: that each of the source's beats is here, the
  newest beat, and uptime over the 24 whole hours, 30 and 365 whole days before the source's newest beat, from the
  source's statistics and the `uptime_hourly` and `uptime_daily` views, within 0.01 percentage points. It exits 3
  when one differs, 1 when it couldn't check.
- **A cutover runbook** in the README, with going back.

Tested with the race detector against MariaDB and TimescaleDB (`internal/importer/rest_test.go`): settings copied,
kept when changed here, replaced when the import wrote them, and absent from the report; both kinds of key, a
damaged one skipped, one off, and one revoked here staying revoked; alert state, and a domain checked here since
left alone; a skipped monitor's history kept, then adopted after Uptime Kuma pruned it; and `-verify` matching, then
reporting a lost beat and a changed daily figure. The REST API takes both kinds of key, refuses wrong ones, and
stops taking a revoked one at once. The importer's existing tests pass with the five new source tables, which were
written from the knex migrations rather than taken from a boot.

Against the built image (`internal/e2e/cutover-e4.sh`, with `kuma-import` from the image and a MariaDB source),
14 of 14 steps of the runbook passed: the dry run's report without values and writing nothing; the import with
settings, the retention default replaced, the skipped monitor's 288 beats kept, and the alert state; both kinds of
key working on the REST API and wrong ones refused; `-verify` exiting 0; `-apply -activate` and a restart with the
imported retention; `-verify` exiting 0 again; and exiting 3 once a beat was deleted here.

Not tested: a real Uptime Kuma database, and imported keys in the API Keys page, which lists them as it lists any.

Known limits:

- `-verify` compares the source's statistics with this service's views. Hours and days before the cutover come
  from the imported statistics on both sides, so it checks the copy there; after it, it checks the beats.
- Monitors' own settings aren't compared, only their history.

## UI enhancements (done)

The web UI is upstream's, with the pages added here. These make it easier to use day to day. Each is a pull request
of its own, in this order, and none changes what the server stores unless it says so.

### U1: Tab titles, unsaved edits, clickable stats, this fork's help links (done)

- Every page of the app sets the tab's title (`src/page-title.ts`), with the number of monitors down first, such as
  `(2) api-gateway - Uptime Kuma`, so that a background tab shows an outage: the monitor's name on its page and
  Edit page, the settings page under Settings, and "Log in" while the login form is shown. Status pages and `/`
  keep setting their own.
- Leaving the Edit Monitor and Edit Maintenance pages with changes not saved asks first, in the app and when the tab
  is closed (`src/mixins/unsaved-changes.js`). The form is recorded once it has loaded, and again after saving, so
  that loading and the defaults filled in don't count as changes. A failed save of a monitor now keeps the form as
  it was, instead of loading the saved monitor over the changes.
- The Up, Down, Maintenance and Paused counts on the dashboard filter the monitor list to those monitors, keeping
  the tags chosen, and the count shows as pressed while its filter is the list's; clicking it again shows all. On
  phones it opens the list. Unknown isn't a filter, as the list can't pick out monitors without a beat.
- The help links point to this fork's README instead of upstream's wiki: Help, the reverse proxy guide, maintenance
  windows and badges. The README has a new "Behind a reverse proxy" section, which says the UI uses Server-Sent
  Events on `/api/realtime`, not WebSockets, with an nginx example.

Tested: `src/page-title.ts` with unit tests. In headless Chromium against the built image
(`internal/e2e/browser/ui-u1.mjs`), 14 of 14 steps passed with no page errors: the login form's title; adding
monitors and leaving without being asked; the title counting the monitor down, and naming a monitor and a settings
page; the Down, Up and Paused counts filtering the list and showing all again; an unchanged Edit page leaving without
asking; a changed one asking, staying when told to and leaving when told to; saving and then leaving without
asking; closing the tab with a change asking; a new maintenance window asking only after a change, and an existing
one not asking without one; a phone's count opening the filtered list; and the reverse proxy link. Checked by eye in
the light and dark themes.

Then (`internal/e2e/browser/ui-followup.mjs`): a save the server refused, for a name over 150 characters, kept the
form and still asked before leaving. The README's nginx example was run in front of the image, and found wrong:
nginx's `$host` leaves the port out, so on any port but 80 and 443 the `Host` didn't match the browser's `Origin`,
and logging in was refused. It now uses `$http_host`. Through it, U2's test passed 9 of 9, live pop-ups included, and
the event stream stayed open for 75 seconds, past nginx's default timeout, with keep-alives coming through.

### U2: Filtering events, and grouping alerts in the UI (done)

- The dashboard's events filter by status, by monitor (a group with everything in it) and by the last hour, day, 7
  or 30 days. The server does the filtering, so that pages and counts are right: `monitorImportantHeartbeatListCount`
  and `monitorImportantHeartbeatListPaged` take an optional filter, `{monitorIDs, statuses, since}`, as their last
  argument, and refuse a bad one. Each browser remembers its filter. New events that arrive are added only when they
  match it.
- Clear All Events moved into a menu beside the filters, shown only to admins. It used to send `clearEvents` for each
  monitor without waiting for the answers, so it reported success before anything was cleared and never reported a
  failure. `clearEvents` now takes `null` for every monitor, in one statement, and the page waits for it. Its
  confirmation now says what it does: events leave the lists, and beats and uptime stay.
- Pop-ups for important beats that arrive within a second of each other are grouped by status, such as "3 monitors
  are down: alpha, bravo, charlie", listing three names and then "and N more" (`src/alert-toasts.ts`). A beat alone
  shows the same pop-up as before.
- Pop-ups can be muted in this browser for a monitor, with a button on its page that viewers have too, which also
  mutes everything in a group, and for tags, under Settings > Notifications. Notifications are still sent, and events
  still listed.

Tested with the race detector: the filter's parsing, and against TimescaleDB, counting and listing events by each
part of the filter and all together, and clearing one monitor's and then every monitor's. `src/alert-toasts.ts` with
unit tests. In headless Chromium against the built image (`internal/e2e/browser/ui-u2.mjs`), 9 of 9 steps passed with
no page errors: three manual monitors going down together showing one pop-up and no others; a monitor muted on its
page left out of the next grouped pop-up; filtering events by status, by monitor and by time, the filter kept after a
reload, and clearing it; a new event outside the filter not added, and one inside it added; Clear All Events from the
menu emptying every monitor's events; and the server refusing a bad filter. U1's test passed again.

Then (`internal/e2e/browser/ui-followup.mjs`): muting a tag in Settings > Notifications left its monitor out of the
next pop-up while another monitor's showed; choosing a group in the events filter listed its own and its monitors'
events and no others; and a viewer had no events menu.

### U3: Keyboard shortcuts and a command palette (done)

- Shortcuts, while logged in, on the app's own pages, and not while typing in a field or with a dialog open
  (`src/shortcuts.ts`, `src/mixins/keyboard.js`): `/` searches the monitor list (on phones, opening it), and in the
  search `Esc` clears it, then leaves it; `n` adds a monitor and `e` edits the one shown, for admins; `j` and `k`
  open the next and previous monitor in the list's order, groups opened; `g` then `d`, `s`, `m` or `,` go to the
  dashboard, status pages, maintenance or settings; and `?`, or Keyboard shortcuts in the account menu, lists them.
- `Ctrl+K` (`⌘K` on a Mac) opens a palette that goes to any monitor, by its path, address or tags, status page,
  maintenance window, settings page or action, with the arrows and Enter (`src/command-search.ts`). It also works
  while typing, and closes it again. Viewers aren't offered the actions and settings that only admins have.

Tested: the shortcuts and the palette's search with unit tests. In headless Chromium against the built image
(`internal/e2e/browser/ui-u3.mjs`), 12 of 12 steps passed with no page errors: `?`; `/`, typing and `Esc` twice;
`j` and `k`; `e` and `g d`; `n`, `g s` and `g m`; `Ctrl+K` to a monitor, and to a settings page with the arrows;
`Ctrl+K` and `Esc` closing it; keys typed in a field not being shortcuts; the account menu's entry; and nothing
before logging in. The test polls for Bootstrap's dialogs, as Playwright's `waitFor` didn't see them open.

Then (`internal/e2e/browser/ui-followup.mjs`): on a Mac, as Chromium reports it, `⌘K` opened and closed the palette
and the list showed `⌘+K`; on a phone, `/` opened the list with the cursor in its search; and a viewer's palette had
no admin actions and only the Security settings page, `n` and `e` did nothing, and the list left them out.

Not tested: a real Mac, and a phone's own keyboard, which has no `/` shortcut to press.

### UA: A prompt for an AI agent to trace a failing monitor (done)

Asked for during U3: when a monitor fails, write up the failure for an AI agent, so that it can trace the issue.

- **Prompt for an AI agent** on the page of a monitor that is down or pending, viewers included, and on the dashboard
  for every monitor down, leaving out a group when a monitor in it is down too (`src/components/AIPromptDialog.vue`).
  It shows the prompt, to copy or download as Markdown.
- The prompt (`src/ai-prompt.ts`) asks the agent to find what's failing, reproduce the check, follow it into the
  service's code, logs and traces, and give the cause, the fix and how to confirm it, asking before changing
  anything. It has the monitor's settings that bear on the check, its state and since when, the latest message, the
  last 20 checks and 10 status changes, the response time and uptime, the certificate, the group's failing monitors,
  the trace ID of the latest failed check, and commands to reproduce the check by type: `curl`, `dig`,
  `openssl s_client`, `nc`, `ping`, `traceroute`, `docker` and `grpcurl`. For several monitors, it asks first whether
  they share a cause.
- Secrets stay out: only an allowlist of fields goes in, header names without values, a database's host and port
  without its connection string, and addresses and messages without credentials, secret-looking query values or
  secret-looking path segments: one after a segment such as `token` or `key`, and long ones of letters and digits
  mixed, such as a Slack, Discord or Telegram token. A long id of letters and digits, such as a UUID, is hidden too.
- The server now sends a failed beat's trace ID to the UI (`traceId`), which it already stored. An all-zero one,
  from a check that wasn't traced, isn't given.

Tested: the prompt with unit tests, including that none of nine kinds of secret on a monitor reach it, and the trace ID
in beats with the race detector. In headless Chromium against the built image (`internal/e2e/browser/ai-prompt.mjs`),
8 of 8 steps passed with no page errors: a monitor that's up not offering it; a failing HTTP monitor with a password
in its address, a token in its query, an `Authorization` header, a body and basic auth offering it, the prompt having
the failure, the commands and a real trace ID, and none of the five secrets; copying and downloading it; and the
dashboard's prompt for two monitors down. U1 to U3's tests passed again.

Then:

- **Given to an agent.** A service whose `/health` answered 503, because its database's name didn't resolve, was
  monitored by the image, and the prompt from its page was given to Claude Code (`claude -p`), with one line of
  context ("the services run in Docker on this machine") and read-only tools: `curl`, `dig`, `nc` and
  `docker ps|logs|inspect|network`. It found the cause from the service's logs, said how sure it was and what it
  couldn't check, gave the fix and how to confirm it, and changed nothing. It also showed two faults, since fixed:
  the prompt said the trace was in Tempo when the image exported none, so it now says the trace is there only if
  traces are exported; and the second `curl` threw away the response's body, which named the failing dependency, so
  it now keeps the first 80 lines of headers and body.
- **The commands, run.** Generated by `src/ai-prompt.ts` and run: Docker's against a stopped container showed its
  state and logs; `nc` reached MongoDB, SQL Server, Redis and MariaDB from their connection strings, and was refused
  on a closed port; `grpcurl` failed against a gRPC server without TLS, as the monitor connects by default, so it now
  takes `-plaintext` unless the monitor uses TLS, and then listed the server's services and called its method.
- **Plain HTTP** (`internal/e2e/browser/ui-followup.mjs`): over an address the browser didn't count as secure, without
  the clipboard API, Copy put the whole prompt on the clipboard. A viewer got the prompt for a failing monitor.

Not tested: a prompt for several monitors given to an agent, and tracing with traces exported.

### U4: The Edit Monitor form (done)

`EditMonitor.vue` was one form of about 4,600 lines.

- **A summary above the form** says what the monitor will do as it's filled in, such as "Requests GET
  https://shop.example.com/health and expects “ok” in the response every 1 minute, and goes down after 3 failed
  checks in a row, 1 minute apart. It alerts Team hook." (`src/monitor-summary.ts`). It warns when the monitor alerts
  no one, and when upside down mode is on.
- **Links to the form's sections** stay in view as it scrolls, mark the section being read, and follow the
  sections the type shows (`src/components/FormSectionNav.vue`, from the form's `h2` headings).
- **Advanced is folded** until opened, and each browser remembers whether it left it open. Monitor Group,
  Description and Tags moved above it, as they aren't advanced. A field the browser won't submit inside the folded
  section opens it and shows the field's message, which the browser can't on a hidden field.
- **Components per type:** 14 of the largest per-type blocks, about 1,170 lines, moved to
  `src/components/monitor-types/`: Kafka producer and its SASL options, RabbitMQ, Globalping, SFTP, NTP, DNS, MQTT,
  RADIUS, system service, PM2, MongoDB, WebSocket authentication and gRPC options. Each injects the form
  (`editMonitor`) and edits its monitor, so their templates moved unchanged. `EditMonitor.vue` is now about 3,600
  lines. The smaller blocks stay, and can move the same way.
- "Websocket Upgrade" was listed twice among the types, and now once. The Globalping hostname's `:pattern` was bound
  to a name that was never defined, so it never applied; the binding was dropped, which changes nothing.
- Scoped styles reach the components with `:deep()`, which `.stylelintrc` now allows.

Tested: the summary with unit tests. `internal/e2e/browser/edit-monitor-snapshot.mjs` records, for every type, the
visible fields (kind, id, label, value, required) and the monitor the form holds after filling each field with a
value of its own; two runs of the same build match. Against the build before U4, every type had the same fields and
the same filled monitor, with Monitor Group, Description and Tags moved; after the split into components, it matched
the build before the split exactly. The type tests, which create each monitor through the form, check its first beat
and reopen it, passed the same before and after: 4a 7 of 7, 4b 6 of 6, 4c 5 of 5, 4e 5 of 5 (with a local forward
proxy), and Globalping in 4f. `internal/e2e/browser/ui-u4.mjs` passed 6 of 6 with no page errors: the summary
following the form; the section links following the type, jumping and marking the section; Advanced folded, then
remembered open; a bad value in the folded section opening it and not saving; and one Websocket Upgrade. U1 to U3's,
the AI prompt's and the follow-up tests passed again. 4e and 4f now open Advanced before filling fields in it.

Not tested: 4d (Docker, system service, PM2, real browser, SFTP and gRPC), which needs a Docker host, D-Bus, Chrome and
a gRPC server set up as `server-go/README.md` describes, though the snapshot covers those types' fields; and
Gamedig and Tailscale ping in 4f, which need a game server and a Tailscale peer.

### U5: The monitor's page (done)

- **A chosen range on the chart**, from Custom range… in its period menu, in the time zone the app shows, and 30 day
  and 1 year periods beside the others. The server answers `getMonitorChartRange(monitorID, from, to)`, with RFC 3339
  times up to a year apart, at the resolution `getMonitorChartData` uses for a window that long: `Store.ChartRange`,
  which `Store.Chart` now calls for the last hours. A chosen range shows all of itself, even where there are no beats.
- **Outages**, a card on the page (`src/components/MonitorOutages.vue`): the monitor's outages in the last 7, 30, 90
  or 365 days, newest first, each with when it began, how long it lasted or that it's going on, and its first error,
  with how many there were and how long it was down in all. `getMonitorOutages(monitorID, days)` finds them in the
  status changes the events list has, from a change to DOWN until the next to UP or MAINTENANCE; PENDING doesn't end
  one. It lists the newest 100, and reads every change, 5,000 at a time, up to 200,000; past that, outages are
  counted from the oldest change read, and the card says from when. The list reloads when the monitor's status
  changes.
- **An outage on the chart:** each has a button that shows it on the chart, with a quarter of its length, and at
  least 15 minutes, either side (`src/outage-range.ts`).
- **The uptime figures** (24 hours, 30 days, 1 year) are buttons that show their period on the chart.
- Both events are open to viewers.

Tested with the race detector: finding outages (PENDING not ending one, MAINTENANCE ending one, one going on, an UP
before any DOWN left out); the events against the fake store, as a viewer, with the window, the totals, the newest
first, and refusing bad windows and ranges; and against TimescaleDB, a range from raw beats minute by minute, the
same points as `Chart` for the same window, and an empty range. The chart test that compares `Chart` with raw data
passed unchanged, as did the REST API's. `src/outage-range.ts` with unit tests. In headless Chromium against the
built image (`internal/e2e/browser/ui-u5.mjs`), 7 of 7 steps passed with no page errors: a manual monitor's two
outages listed newest first, the newer going on; the list updating when it came back up; an outage shown on the
chart; a chosen range shown and a backwards one refused; the uptime figures showing 30 days, a year and 24 hours,
which loaded without errors; and a shorter window. U1 to U4's, the AI prompt's and the follow-up tests passed again.

Known limits:

- Outages come from the events list, so clearing a monitor's events, or Clear All Events, clears its outages too,
  and an outage that began before the window starts at its first change in it, or isn't shown when it ended in it.
- A chosen range far back has only what the retention kept: raw beats for up to 6 hours, then hourly and daily
  statistics.

Not tested: a range of more than 30 days, which reads the daily statistics; the chart test covers that window for
`Chart`.

### U6: Editing many monitors at once (done)

- **Editing the selected monitors together:** Edit… in the list's Actions menu, beside pause, resume and delete
  (`src/components/BulkEditDialog.vue`). It sets the interval, retry interval or retries, adds or removes
  notifications, and adds a tag with a value or removes a tag with all its values, showing how many of the selected
  monitors have each one and, before saving, what will change. Only what's changed there changes.
  `bulkEditMonitors(ids, changes)` checks every monitor first, so a value one refuses changes none of them, and
  refuses notifications and tags that don't exist, and asking to add and remove the same one. Up to 1,000 monitors.
- **Importing monitors from CSV or YAML** (`src/components/ImportMonitorsDialog.vue`), from Import beside Add New
  Monitor, on the Add page, or the command palette (`/add?import=1`). The fields are the REST API's, read the way it
  reads them, so a misspelled field is an error rather than a default. CSV has a header row, empty cells leave the
  default, lists take values separated by `;` and `headers` takes JSON (`src/monitor-import.ts`); YAML or JSON is a
  list, or a map with a `monitors` list. Check runs `importMonitors(monitors, true)`, which shows each row as ready or
  what's wrong with it; Import adds them, only once all are valid. They get the default notifications, as from the
  REST API, and `parentId` puts one in a group: a group in the same file by its `id`, or else one on the server.
  Groups in the file are added before what's in them, and Check refuses a loop of groups, an `id` twice, and a push
  token in use or twice in the file. Up to 1,000 at once. The web UI reads js-yaml, now a direct dependency.
- **A paused group stops the monitors in it.** The list already showed them as paused, but the scheduler kept
  checking them, writing beats and alerting. It now checks a monitor only while it and every group above it are
  active: pausing a group stops everything in it however deep, resuming it starts what's active in it at once, and
  after a restart a paused group's monitors stay stopped. Moving a monitor into or out of a paused group, or
  deleting the group, applies the same rule. Clearing a group's data suspends only the group (`Suspend`), so it
  doesn't start the monitors in it.
- **An import shows at once.** `kuma-import -apply` writes to the database directly, so the running service didn't
  know: open pages needed a refresh, and `-activate` a restart. It now ends with `NOTIFY kuma_imported`; the
  service `LISTEN`s on its own connection, out of the pool, and then reloads maintenance windows, gives the
  scheduler the monitors as they are (`Sync`), and sends every open page its lists again, as at login. It also
  reloads each time it starts listening, the first time too, so an import that finished while it wasn't is picked
  up. Every monitor a reload sees, paused ones too, gets the state saved in the database, as at startup, so that one resumed by `-activate` or by hand later doesn't alert on its first check; a later reload refreshes a state that came from the database, never one the monitor's own checks made, and leaves alone a monitor whose data is being cleared; and an import that fails notifies too, since it may have written part of its data. A reload has 2 minutes; a failed one, or a lost connection, waits 5 seconds and listens again. `Sync` takes the scheduler's
  `Generation` from before the monitors were read, and leaves alone any monitor changed here after it, by a pause,
  an edit or a delete, so an older snapshot can't undo them; it installs every monitor before starting any, so a
  monitor can't start while its new group is still missing, restarts only those that changed, and stops and
  forgets those gone. Everything that must stop is cancelled before any is waited for, so a slow check elsewhere
  can't let a monitor in a newly paused group record a beat meanwhile; pausing a group does the same. Tested with the race detector against TimescaleDB: the listener hearing imports, retrying a
  failed reload, and recovering after its connection is closed; `Sync` with stale snapshots and groups; and open
  pages getting their lists. A built service reloaded on `NOTIFY kuma_imported` from `psql`. Not tested: a real
  `kuma-import -apply` from MariaDB with a page open, and on Kubernetes.
- **Pausing or resuming many monitors is one request.** The list's Actions sent `pauseMonitor` or `resumeMonitor`
  once per monitor, and the server answered each with its own list update, after loading the whole monitor tree
  twice. A connection's events run in order, so with a database a millisecond away 190 monitors took seconds, and
  the list re-sorted itself monitor by monitor, paused ones moving up one at a time. `setMonitorsActive(ids,
active)` changes them in one statement and sends one update, with the monitors in the groups among them. The
  toast now waits for the answer, and says what failed; after 30 seconds without one, the actions work again and
  the toast says the result is unknown, and a late answer changes nothing (`src/bulk-active.ts`). Tested: the event
  with a group's monitors, the store's statement against TimescaleDB, the request's answers and time limit, and in
  a browser, 190 monitors resumed in one list update with the database a millisecond away. Not tested on
  Kubernetes.
- **Easier to choose monitors.** Each monitor's box is bigger (1.3rem, as is the header's), in a label as tall as
  the row, so the space around it ticks it too. Once one is ticked, a click anywhere on a row ticks or unticks it
  rather than opening the monitor, and ticked rows are highlighted; the group's open and close button still works.
  With nothing ticked, rows open their monitor as before, and Ctrl or Cmd, Shift or a middle click are left to the
  browser, for a new tab, even while choosing; Enter on a focused row opens it too, since the keyboard ticks with
  the box (`src/row-click.ts`). The row is Vue Router's `custom` link, so that
  the click can be taken over. Tested: the click's decision by unit tests; and in a browser against a built
  service, by hand-written checks, the box (21 px) and its target (39 by 56 px), ticking from the space around the
  box, a row click ticking while choosing and opening otherwise, the group button, and the highlight in light and
  dark mode, a Ctrl-click opening a new tab while choosing, and Enter opening the monitor. Not tested: touch screens.
- **A resizable, sortable monitor list.** The dashboard's list has an edge to drag (`src/pages/Dashboard.vue`), or
  move with the arrow keys, Home and End; Enter or a double click resets it; right to left, the edge and its arrows go the other way. It stays between 280 pixels and what
  leaves the monitor's page 360, at most 70% of the row, and is remembered per browser (`src/panel-width.ts`). The
  list's bars follow as it's dragged. A sort menu beside the filters (`src/monitor-sort.ts`) sorts by name (numbers
  in order), status (down, pending, maintenance, up, then no beat), the last 24 hours' uptime (unknown last), or
  recently added, each reversible, with paused monitors last; it's remembered per browser. **Custom order** is
  everyone's: an admin drags a monitor above or below another of the same group, a line showing where, or moves the focused one with Alt and an arrow, and
  `setMonitorOrder(parentId, ids)` stores it in the new `monitor.sort_order` column, in one transaction that
  refuses monitors from another group and puts any left out after the others. A monitor moved to another group starts unordered there. The order is the REST API's and the export's `sortOrder`; editing a monitor
  keeps it, and the scheduler ignores it, so reordering restarts no check. A group's middle still takes a monitor
  into it. Tested: the sorts, moves and widths by unit tests; `setMonitorOrder` against TimescaleDB and through the
  event; and in a browser against a built service, the edge (default, dragged, its limits, kept after a reload, the
  keys, the reset), each sort, and dragging in custom order, kept on the server and after a reload, near the rows'
  edges too, with a group's middle still taking a monitor in. Not tested: touch screens, where HTML drag and drop
  doesn't start, so only the keyboard reorders there.
- **Exporting the selected monitors** (`src/components/ExportMonitorsDialog.vue`), from Export… in the list's Actions
  menu. `exportMonitors(ids, {groups, children})` returns them in the REST API's format with their credentials, so
  only admins can. `groups` adds the groups they're in up to the top, and `children` everything in a chosen group;
  a monitor whose group isn't exported loses its `parentId`, so the file is complete on its own. Groups come before
  what's in them. The UI saves it as YAML or JSON (`src/monitor-export.ts`), and logs who exported how many.
- **Test in the Edit Monitor form** runs one check of the monitor as the form has it, saved or not, and shows the
  status, the time it took and the message beside Save. `testMonitor(monitor)` runs the server's checker as the
  first check of a new monitor, so a failure is DOWN whatever the retries, and upside-down mode applies. It's cut to
  25 seconds, and stores nothing: `check.DryRun` keeps a DNS monitor's last result and a browser monitor's screenshot
  from being saved. Push monitors have nothing to test, so they have no button.
- **`npm run dev`** works with the Go backend: Vite sends the backend's paths to it, at `http://127.0.0.1:3001` or
  `KUMA_BACKEND`. The web UI had sent them to port 3001 itself, which the backend refused, as it sends no CORS
  headers. The README says how to run it.
- The three events are for admins only.

Tested with the race detector, against the fake store: the bulk edit (interval and retries, notifications and tags
added and removed, other notifications and tags kept, repeated IDs, an invalid interval changing none, missing
monitors, notifications and tags, both adding and removing, nothing to change, and a viewer refused); the import (a
dry run writing nothing, a misspelled field, a missing URL and a parent that isn't a group each named on its row and
nothing imported, then the import with defaults, the group and the default notifications, an empty file, and a viewer
refused); and the test (up, down whatever the retries, upside-down, the timeout cut to 25 seconds, nothing saved, push
and an invalid form refused, a viewer refused, and a server without a checker). `src/monitor-import.ts` with unit
tests. In headless Chromium against the built image (`internal/e2e/browser/ui-u6.mjs`), 8 of 8 steps passed with no
page errors: a CSV with a bad number refused before sending, and one with a keyword monitor that has no keyword
refused by the server on its row, with nothing added; a valid CSV checked, needing another check after an edit, then
imported with its intervals, keyword, status codes and default notification; YAML from a file into a group; three of
four monitors given an interval, a notification and a tag, and an old notification taken off, the fourth and their
other settings unchanged; an invalid interval changing none, and a tag taken off one monitor; Test showing up with the
time and message, then down with the error, an invalid form not tested, nothing saved, and no button for push; and the
command palette opening the import. U1 to U5's, the AI prompt's and the follow-up tests passed again, and the Edit
Monitor form's snapshot of every type was as in U4.

Known limits:

- An import that fails while writing, such as on a database error, leaves the monitors before it added. Each
  monitor is checked first, so a value can't cause that. A bulk edit is one transaction.
- The import takes the REST API's fields: not notifications or tags by name, nor a group by name.
- A browser monitor's Test takes a screenshot that isn't kept.

Not tested: Test on monitor types that need servers this machine doesn't run (Docker, gRPC, a real browser, and the
others 4d lists), and the import of the types beyond HTTP, keyword and TCP port.

### U7: Viewers, the first run and phones (done)

- **A pass through every page as a viewer**, with a script that lists what each page lets a viewer click and the
  events the server refuses. What it found, now fixed:
  - The status page showed a viewer **Edit Status Page**, and `?edit` opened the whole editor, whose Save and Delete
    could only fail. It reads the session without the realtime connection, so it never learned the role; it now
    takes the role from the session, and a viewer gets neither.
  - Going to a settings page a viewer doesn't have threw errors while it was sent away (the header read the title of
    a page the viewer's menu lacks).
  - A viewer sent away from a page that edits went to the dashboard without a word. Now they go to the nearest page
    that only shows (a monitor's edit page to the monitor's page, settings to Security, a maintenance window to the
    list, a new status page to the status pages), with a note that the account can only view.
  - The dashboard, a monitor's page, the status pages and maintenance lists had no buttons a viewer couldn't use.
- **A "View only" badge** in the header for viewers, explaining that an admin makes changes; on phones, a line above
  the navigation.
- **The first run's guide** (`src/components/GettingStarted.vue`, `src/getting-started.ts`): on the dashboard, three
  steps, ticked as they're done: add a monitor (or import), get alerted, share a status page, with the next step's
  button standing out. It starts only in a browser that sees a server with no monitors, so existing installations
  never get it, and stays until its steps are done or it's hidden, which this browser remembers. A viewer of a
  server with no monitors is told that an admin adds them, and the empty list no longer offers a viewer "add one".
- **Phones:** the dashboard's five counts take two short rows instead of most of the first screen. Every form's
  sticky Save bar stayed under the bottom navigation while scrolling, so Save was hidden until the form's end; it now
  sits above it, nearly opaque. The Edit Monitor form's Test result has a line of its own there.

Tested: the guide's states with unit tests. In headless Chromium against the built image
(`internal/e2e/browser/ui-u7.mjs`), 13 of 13 steps passed with no page errors: a new server's guide at its first step
and the empty list; a viewer of an empty server told that an admin adds monitors, with no guide; each step ticked as
it was done; hiding it lasting after a reload; the last step, done away from the dashboard, finishing it for good; no
guide in a browser that first saw the server with monitors; the viewer's badge and its explanation, which admins
don't get; a viewer sent from five pages that edit to the nearest one, with the note, and to Security from another
settings page; a viewer's status page with no Edit button and `?edit` not opening the editor, while an admin's
still does; no buttons that change things on a viewer's dashboard, monitor page, status pages and maintenance list;
and at 375 pixels, the counts in two rows under 200 pixels high with no sideways scrolling, Save above the navigation
mid-form, Test's result on its own line, and a viewer's note above the navigation. U1 to U6's, the AI prompt's and
the follow-up tests passed again.

Known limits:

- The guide is per browser: a second admin, or another browser, of a server set up elsewhere doesn't get it.
- The status page's editor is still in the page for a viewer who goes to it with `?edit`; it just doesn't open.

Not tested: real phones (Chromium's phone emulation at 375 by 760 only), and the dark theme on phones.

### U8: Bootstrap 5.3 and accessibility (done)

- **Bootstrap 5.3.8** (from 5.1.3) and **Font Awesome 6.7** (from 5.15), with Popper 2.11. The theme sets
  `data-bs-theme`, and `vars.scss` gives Bootstrap's dark colour mode the dark theme's colours, so Bootstrap styles
  dark form fields, checkboxes, the close button and pagination, whose own dark rules are gone. Rules with colours of
  their own (menus, the list filter, tables, dialogs, switches) stay. Every icon name the templates use still
  resolves, under Font Awesome 6's old names. Before and after, screenshots of ten pages and three open menus and
  dialogs, light and dark, differed only by a pixel's shift in text and by the beats and charts that moved between
  them.
- **Menus from the keyboard:** the account menu's toggle was a `div`, which the keyboard couldn't reach; it's a button
  with a label. The status and tag filters' items answered only clicks: they're checkboxes now, which Enter and Space
  toggle, saying whether they're chosen, and Escape closes the filter.
- **The monitor list from the keyboard:** a group's arrow was a `span`, so groups couldn't be opened or closed from the
  keyboard; it's a button that says whether the group is open. A monitor dragged into a group keeps focus, its group
  opens to show it, and a message says where it went. Viewers can't start a drag, which could only fail.
- **Statuses by more than colour:** the uptime pill in the list has an icon for down, pending and maintenance, and
  says the status to screen readers. In the heartbeat bar, a down beat has a notch, a pending one is shorter and a
  maintenance one is an outline (`src/beat-shape.ts`); its label counts pending and maintenance too, and is
  translated.
- **The heartbeat bar's contrast:** up (1.7:1) and pending (2.1:1) beats were too pale on white, and maintenance
  (2.9:1) too dark on the dark theme, for WCAG's 3:1. On white, up and pending beats have a darker edge (3.4:1 and
  3.3:1); on the dark theme, maintenance is a lighter blue (5.4:1).
- **Focus that shows:** the status page's editable text and its CSS editor showed no focus ring.

Tested: the beat shapes and their contrast with unit tests. In headless Chromium against the built image
(`internal/e2e/browser/ui-u8.mjs`), 11 of 11 steps passed with no page errors: Bootstrap 5.3's colour mode following
the theme, with a dark field in the theme's colour; Font Awesome 6's icons under old names; the account menu opened
with Enter, its first item reached with the arrow keys and closed with Escape; the status filter's Down chosen with
Space and unchosen with Enter, and closed with Escape; a group opened and closed with Enter, without following its
link; a monitor dragged into a closed group, the group opened, focus on the monitor, and the message; the down pill's
icon and words; the bar's notched down beats and the up beats' edge, read from its pixels, and its label; a viewer's
drag refused; and the focus rings. U1 to U7's, the AI prompt's and the follow-up tests passed again, and the Edit
Monitor form's snapshot of every type was as in U6.

Known limits:

- The filter menus open when their button gets focus, as before, rather than on Enter, and have no arrow keys.
- Dragging into a group has no keyboard equivalent in the list; the form's Monitor Group field does it.
- The heartbeat bar's empty slots stay pale: they hold no data.

Not tested: screen readers themselves (the names, roles and states were checked in the page), and Windows' high
contrast mode.

### U9: Status page incidents and subscriptions (done)

- **Incident templates:** the incident form's **Templates** menu fills the title, content and style from a saved
  template or one of four starters, in the UI's language. **Save as a template** keeps the form, one template per
  title, shared by every page (`incident_template`).
- **Coming maintenance:** a window linked to the page shows a banner from a week before it starts, with its start
  and end in the reader's time zone and how soon it starts (`upcomingMaintenanceList` in the page's data).
- **Subscribers** (`status_page_subscriber`), set up in the page editor's **Manage subscribers** dialog:
  - **Email:** the page names an SMTP notification to send through (`status_page.subscribe_notification_id`),
    which needs the Primary Base URL for its links. Visitors subscribe on the page, and confirm from an email's
    link, which the page posts back when it opens, so a mail scanner that follows links doesn't confirm or
    unsubscribe anyone. Each email links to unsubscribing, which asks first, and has RFC 8058's one-click
    unsubscribe. Subscribing answers the same whether the address is new or not, sends one confirmation per 10
    minutes, is limited per address and overall, and unconfirmed addresses go after two days.
  - **Webhooks**, added by admins, get `incident.created`, `incident.updated` and `incident.resolved` as JSON.
    Their addresses are encrypted like other secrets.
  - An incident goes to subscribers when it's posted with **Send to subscribers** ticked, which an inline edit
    ticks only if they were told of it; its resolution goes only to those, when it was sent (`incident.notified`).
    Messages go from a queue, in order. The dialog shows each subscriber's last send and error.
- `notify.SendMail` sends one email through a notification's server and sender, without its recipients, headers or
  templates, so an incident's text is never read as a template.

Tested: the store's subscribers, their tokens and sealed webhooks, and templates, against TimescaleDB;
`SendMail`'s recipient, headers and refusal of line breaks against a test mail server; and through the realtime API
and public routes against a test mail server and webhook receiver (`internal/e2e/subscriptions_test.go`): the
sender's checks, one confirmation however often an address subscribes, nothing to an unconfirmed address, a
confirmed one's update and resolution with unsubscribe links, the webhooks' order, a failing webhook's error, a
quiet incident's resolution sent to no one, one-click unsubscribe, and the coming windows within a week, soonest
first. The link parsing, times and SMTP filter with unit tests. In headless Chromium against the built image
(`internal/e2e/browser/ui-u9.mjs`), 10 of 10 steps passed with no page errors: the dialog choosing the sender and
adding a webhook; a visitor subscribing, and the emailed link confirming it and leaving the address bar; asking
again sending nothing; the coming window's banner in Hong Kong time with the later one left out; a starter filling
the form and a saved template filling it again; posting to two subscribers by email and webhook; the resolution
following; the emailed unsubscribe link asking, then unsubscribing; a viewer refused the subscribers and
templates; and at 375 pixels in the dark theme, the banner and form without sideways scrolling.

Known limits:

- Emails are plain text, in English. Templates have no placeholders.
- A failed send isn't tried again, and messages still queued when the service stops are lost.
- Subscribers get incidents only: not maintenance windows, nor monitors going down.
- Only admins add webhooks, so a visitor can't make the server call an address.

Not tested: a real mail provider's delivery and its one-click unsubscribe, and pages with thousands of
subscribers.

### S1: Managing single sign-on in the UI (done)

- **Settings > Single Sign-On**, for admins: GitHub login (the OAuth App's client ID and secret, the organization,
  admin and viewer teams, and GitHub Enterprise Server's addresses) and whether passwords work, saved in
  `login_config` and applied at once, without a restart. The page shows the callback URL to register, and the last
  GitHub login. The client secret is sealed like other secrets, and never sent to browsers: an empty field keeps it.
  Other instances read the settings every 30 seconds.
- **Environment variables still win:** with `KUMA_GITHUB_CLIENT_ID` or `KUMA_PASSWORD_LOGIN` set, the page shows
  their settings read-only, without the secret, and saving is refused.
- **No lockouts:** passwords can only be turned off by an admin logged in with GitHub, with GitHub's settings as
  they logged in with them. While passwords are off, GitHub's settings can't change. No save may leave no admin who
  can log in. Changing the app, organization or teams makes every GitHub account due for a check of its teams at
  its next request; turning GitHub off ends their sessions at their next request.
- The login code reads GitHub's settings once per login or check, so a change mid-login can't mix two settings.
- `HiddenInput` put its `id` on its wrapper, so the labels of 15 secret fields in the monitor and notification
  forms named nothing; the id is on the input now.

Tested: the store's settings, sealed secret and admin checks against TimescaleDB; with Go tests against a fake
GitHub, the form's checks, a save applying at once, an admin logging in with GitHub straight after, turning
passwords off only from that login, GitHub held while they're off, teams changing making accounts due, GitHub off
keeping its settings, no change leaving no admin, and the environment's settings shown read-only and kept; the
team parsing with unit tests. In headless Chromium against the built image with a fake GitHub
(`internal/e2e/browser/sso.mjs`), 7 of 7 steps passed with no page errors: a password admin unable to turn
passwords off; setting up GitHub in the form, with the callback URL, the secret kept and teams lowercased, and the
login page offering GitHub at once; an admin team's member logging in; that admin turning passwords off, and the
password form and sign-in gone; GitHub's org change refused while passwords are off; a viewer team's member a
viewer, sent from the page and refused its event; and GitHub off stopping its login, keeping its settings, and
ending the GitHub session. With environment variables, `github-7b.mjs` (both runs) and `security-e2.mjs` passed
again, as did `accounts-e1.mjs`, `accounts-5a.mjs`, `roles-7a.mjs` and U4, U7 and U8's tests. The Edit Monitor
form's snapshot differed from U8's only by the secret fields' ids and labels.

Known limits:

- GitHub, Google (S2) and one OpenID Connect provider (S3) are the providers: there's no SAML.
- The client ID and secret aren't checked when saved: logging in with GitHub checks them.
- The page in its read-only state, with environment variables, was only tested by Go tests, not in a browser.

### S2: Logging in with Google (done)

- **Google login** (`internal/ui/google.go`) by OpenID Connect with PKCE and a redirect URI under the Primary Base
  URL, or else the address the browser used. Who may log in comes from Google's userinfo: a verified email, and
  the Workspace domain from the `hd` claim, which Google sets. Admin emails and viewer emails decide the role, and
  a viewer of `*` lets in everyone of the listed Workspace domains, which `*` requires, so that it can't mean any
  Google account.
- **Accounts** are named by their email (`google_sub`, `google_email`, `google_domain` on `app_user`), and a login
  is refused rather than taking over a password account with that name, whatever its case. An email that moves to
  another person renames the old account, as GitHub logins do.
- **A daily check** with the refresh token Google sends at the first consent, sealed like other secrets: Google
  refusing it (`invalid_grant`, as after a Workspace suspension, deletion or revoked access) logs the account out;
  Google being unreachable keeps its role and tries again in 10 minutes. Without a refresh token, the lists decide.
  Changing the lists or client makes every Google account due at its next request; turning Google off ends their
  sessions there.
- **Settings > Single Sign-On** has a Google section beside GitHub's, with the redirect URI to register. Passwords
  can be turned off from an admin's GitHub or Google login, and that provider's settings are held while they're
  off. Settings > Security shows a Google account's email, domain, role and last check, and Settings > Accounts its
  method. `KUMA_GOOGLE_*` environment variables set it instead, read-only in the page. `KUMA_GOOGLE_ENDPOINT`
  replaces Google's addresses, for tests.

Tested: the store's Google accounts (the password account's name kept, the sealed refresh token kept when a login
brings none, renaming, roles not settable here) and settings against TimescaleDB; with Go tests against a fake
Google that checks PKCE, the client and the redirect URI as Google does, the settings' checks, the authorize
request, an admin and a viewer by email and a domain's viewer by `*`, refusals of a personal account with the
domain's address, another domain, an unverified email and a password account's name, a wrong state, no password
or two-factor changes for a Google account, passwords turned off only from a Google admin's login and Google held
then, a revoked account logged out at the daily check while others stay, no call to Google within the day, a
list change applying at the next request, and Google off ending its sessions. In headless Chromium against the
built image (`sso.mjs`, now with a fake Google too), 12 of 12 steps passed with no page errors, five of them
Google's: setting it up in the form, with `*` refused until a domain is entered and the login page then offering
Google; an admin email logging in, with Security saying how; a Workspace member a viewer by `*`, and a personal
account with the domain's address refused with its message; a Google admin turning passwords off and back on; and
the account in Settings > Accounts.

Known limits:

- An account whose person had already consented to the client before has no refresh token, so only the lists
  check it daily, not Google.
- The ID token isn't verified: the userinfo endpoint, asked over TLS with the access token, says who it is.

Not tested: real Google, and a Workspace account suspended for real.

### S3: Logging in with OpenID Connect (done)

- **One OpenID Connect provider** (`internal/ui/oidc.go`), named for the login page's button: discovery from the
  issuer, which must match the document's; the authorization code flow with PKCE, state and nonce; the client
  authenticated with HTTP Basic, or in the form when that's all the provider supports; and the ID token verified
  with the provider's JSON Web Keys (RSA of 2048 bits or more, and EC on P-256, P-384 and P-521) with
  `golang-jwt/jwt/v5`, now a direct dependency: signature (HMAC and `none` refused), issuer, audience, `azp` with
  several audiences, expiry, issued-at and nonce. Keys are read again for an unknown key id, at most once a minute.
- **Roles** from groups, in a configurable claim of the ID token or userinfo (which only counts for the same
  subject), compared as written since some providers' are ids; and from emails, only those the provider says are
  verified. A viewer group of `*` lets in everyone the provider logs in.
- **Accounts** keyed by "issuer subject" (`oidc_sub`), named by email, preferred username or subject, never taking
  over another account's name; only a verified email moves from one account to another. A daily check with the
  refresh token, when there is one: refused logs out, unreachable keeps the role and tries again every 10 minutes
  until a check succeeds, new groups apply (a groups claim that the ID token and a userinfo answer both leave out is no groups, as Keycloak leaves it out
  for someone in none), a rotated token is kept. Another issuer logs the old one's accounts out.
- The provider's endpoints, from discovery and from any redirect, must be https, or http on this machine.
- **Settings > Single Sign-On**'s OpenID Connect section, with a **Check** of the issuer's discovery document, and
  scopes and the groups claim under **Scopes and claims**. The issuer must be https, or http on this machine for
  trying a provider. Security, Accounts and the login page show the provider by its name. `KUMA_OIDC_*`
  environment variables set it instead.

Tested: the store's accounts (no takeover of a password or Google account's name, the sealed token rotated and
kept) and settings against TimescaleDB. With Go tests against a fake provider with RSA and EC keys: the settings'
checks and the issuer check; the authorize request's scope, nonce, PKCE and redirect; an admin by group, a viewer
by group with the email and groups from userinfo, a viewer by email with an EC-signed token; refusals of an
unverified listed email, someone in no group, another account's name and a wrong state; the Security page and no
password changes; passwords turned off only from the provider's admin and its settings then held; at the daily
check, a revoked account logged out, a role changed by new groups and a rotated refresh token kept; and another
issuer logging the old one's accounts out. ID token verification refused another key's signature, HS256 with the
client secret, no signature, an unknown key id, another issuer, another audience, an expired token, one without
expiry, another nonce, no subject, and another authorized party; and a discovery document for another issuer. In
headless Chromium against the built image (`sso.mjs`, with a fake provider that signs RS256 tokens), 17 of 17
steps passed with no page errors, five of them this provider's: the issuer check failing on a wrong address and
finding the right one, saving, and the login page's button by name; an admin group's member logging in with
groups from userinfo, and the Security page; a viewer group's member a viewer, and someone in neither refused;
the provider's admin turning passwords off and on; and the account in Settings > Accounts under the provider's
name.

Known limits:

- One OpenID Connect provider at a time.
- Logging out doesn't log out of the provider (no RP-initiated logout).
- Only an email the provider says is verified counts, so with Entra ID, which doesn't send `email_verified`, roles
  come from groups, and accounts are named by their preferred username.

Not tested: real providers (Okta, Entra ID, Keycloak and the others).

### Fixes from a code review

A review of the branch found ten faults, all fixed, each with a test:

- **OpenID Connect emails:** an email without `email_verified` gave the role its email list granted, so with a
  provider that lets users set any email, anyone could be an admin. Only verified emails count now. The daily
  check kept the old email when the provider said the new one wasn't verified; it drops it now.
- **Events and outages:** clearing events erased the important flag outages are found from, so it emptied every
  monitor's outages. Clearing now records when (`event_clear`), and the event list starts there. `clearEvents`
  with a missing or null monitor cleared every monitor's: clearing all now takes `"all"`. Outages now include one
  that began before the days and lasted into them, with only the part within counted as downtime.
- **Subscriptions:** a confirmation link worked after the two days its email promises, until someone else
  subscribed. It expires when confirmed now.
- **Single sign-on with several instances:** an instance still on replaced login settings could check an account
  with them and record it for a day. Checks now read the settings again if another instance saved others, and are
  only recorded while the settings they used are still the saved ones.
- **Editing many monitors:** each monitor was written on its own, so a failure partway left some changed. It's one
  transaction now, which writes only the fields it changes.
- **OpenID Connect names:** two logins taking one name at once gave a generic failure instead of "name taken".
- **Testing a monitor:** each test with new client certificate or proxy settings kept a transport for good. A
  test's isn't kept now, and the kept ones are bounded.

- **Logins during a settings change:** a GitHub, Google or OpenID Connect login that read the settings before an
  admin changed them could save the old role after the change expired every account's check, and keep it for a
  day. A login now uses one reading of the settings throughout, and its account's save takes a lock that saving
  the settings takes alone, then checks the settings are still those: if not, nothing is saved and the person is
  asked to log in again.

Not tested: several instances against one database, for the login settings fix (the store's part is tested).
