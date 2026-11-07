# kuma-analytics

The Uptime Kuma backend, rewritten in Go and still in progress. It runs HTTP, keyword, JSON query,
TCP port, ping, DNS, push, group, manual, WebSocket, PostgreSQL, MySQL/MariaDB, SQL Server, Oracle, MongoDB, Redis,
MQTT, RabbitMQ, Kafka producer, NTP, SNMP, SMTP, SIP, RADIUS, Steam, Docker container, system service, PM2, SFTP, gRPC,
real browser, Globalping, GameDig and Tailscale ping monitors, every type the Node.js server had, stores raw beats in TimescaleDB, and serves monitor config, history, charts,
percentiles and dashboard stats over REST, plus new beats over Server-Sent Events. It serves public
status pages and badges, exports traces and metrics with OpenTelemetry, and ships Grafana dashboards.

**Status:** this is now the only backend, because the Node.js server and MariaDB support have
been removed. Enhancements beyond what the Node.js server did are planned in [`PORTING.md`](PORTING.md).

[PORTING.md](PORTING.md) records what has moved from the Node.js server, phase by phase, and what's left.

## Web UI

The Docker image, built from the repository root's `Dockerfile`, embeds the web UI from `src/` and serves it
on the same port as the API. `/api/v1/` and `/healthz` are the REST API, `/api/auth/`, `/api/setup` and
`/api/realtime/` are the UI's own API, `/api/push/` receives push monitors' beats, `/status/`,
`/api/status-page/` and `/api/badge/` are public, and every other path is the UI. A plain `go build` leaves the UI out.

On first start, open the UI and create the account, or set `KUMA_INITIAL_ADMIN_USERNAME` and
`KUMA_INITIAL_ADMIN_PASSWORD` to have it made at start. Setup is only offered for **10 minutes after the
service starts**, so that a stranger can't claim a server someone forgot to set up; restart the service if
the window has passed. The account made at setup is an admin, which can do everything.

Accounts can also be **viewers**, which can look at monitors, beats, maintenance windows and status pages, but
can't change anything, and aren't sent credentials. The server enforces this: a viewer's page gets monitors without
URLs' credentials and query strings, headers, bodies, connection strings, push tokens or passwords, and empty lists
of notifications, proxies, Docker hosts and remote browsers, and every event that would change something, or read
settings or API keys, is refused. The UI hides those buttons, and sends a viewer back from settings and forms,
except Settings > Security, for their own password, two-factor login and GitHub teams.
Accounts other than the setup one come from logging in with GitHub.

**Settings > Accounts** lists every account, for admins: its role, whether it logs in with GitHub or a password, its
last login and its open sessions. An admin can end an account's sessions, remove it, and change a password account's
role; a GitHub account's role follows its teams. Removing a GitHub account lasts only until its next login while it's
still in the teams. No change may leave no admin who can log in, and admins can't remove or demote themselves. Each
change is logged with who made it.

### A prompt for an AI agent

When a monitor is down or pending, its page has **Prompt for an AI agent**, and when any are down the dashboard has
one for all of them. It writes up the failure in Markdown, to copy or download and give to an agent such as Claude
Code: what the monitor checks and how often, its state and since when, the latest message, the recent checks and
status changes, uptime, the certificate, the trace ID of the latest failed check, which leads to its trace in Tempo,
and commands to reproduce the check, such as `curl`, `dig`, `openssl s_client` or `nc`. For several monitors, it
asks first whether they share a cause.

It's built in the browser from an allowlist of the monitor's fields, so passwords, tokens, header values, bodies,
connection strings, push tokens and keys never go in. User names, passwords and query values whose names look like
secrets are removed from addresses and messages, and the commands say where to put them back.

### Logging in with GitHub

With a GitHub OAuth App, the login page offers "Log in with GitHub". Membership of the organization's teams decides
who may log in, and as what, at each login and again once a day while a session is used, so that someone who leaves
a team loses access within a day, and a role that changed takes effect. An open page checks its session every five
minutes, so it needs no new request to find out. When GitHub can't be reached, the account keeps its role, and is
checked again ten minutes later. Two-factor login is left to GitHub, where
the organization can require it.

1. In the organization's settings, under Developer settings > OAuth Apps, register an app with the callback URL
   `https://<this server>/api/auth/github/callback`.
2. Set `KUMA_GITHUB_CLIENT_ID`, `KUMA_GITHUB_CLIENT_SECRET` and `KUMA_GITHUB_ORG`, and the team slugs:
   `KUMA_GITHUB_ADMIN_TEAMS` for admins and `KUMA_GITHUB_VIEWER_TEAMS` for viewers, comma-separated. A viewer
   team of `*` lets every member of the organization look.
3. Once it works, `KUMA_PASSWORD_LOGIN=false` turns the password off, which then only stays as a way in when
   GitHub is down, after it's turned back on.

The app asks for the `read:org` scope, to read team membership. Each GitHub user gets an account named `@login` at
their first login, so password accounts can't start with `@`; `kuma-import` drops a leading `@` from the imported
account's name, and says so in its report. The token is stored, as the password hashes are, to
check the teams again; it's never sent to browsers. Setup isn't offered once someone has logged in with GitHub.
`KUMA_GITHUB_URL` and `KUMA_GITHUB_API_URL` point at a GitHub Enterprise server instead.

What works in the UI: logging in and out, two-factor login, changing the password, API keys, the dashboard with live beats, uptime and
response times, the event list, adding, editing, pausing and deleting the monitor types above, charts, notifications (add, edit, test, delete, defaults), maintenance windows with public
holidays, tags, certificate details and expiry alerts, status pages with incidents, badges, settings, and Monitor History.

Everything else answers "This isn't supported by this server yet". In particular:

- Saving a monitor with another type, a proxy, or an authentication method other than basic or bearer is refused, so that a check never runs differently from what the form shows. Basic and bearer
  authentication are saved as an `Authorization` header, and show up under Headers when you edit again.
- Form settings this service doesn't have, such as certificate expiry notifications, aren't saved.
- Of **Settings > General**, the server timezone comes from `KUMA_TIMEZONE` and can't be changed there. The DNS cache,
  Prometheus and "API and MCP" switches are gone: the image has no nscd, OpenTelemetry replaces `/metrics`, and the
  REST API and MCP server are always on, guarded by their keys.
- **Settings > About** shows a notice when this fork has a newer release: a `v1.2.3` tag in
  [pantheon-lab/uptime-kuma](https://github.com/pantheon-lab/uptime-kuma/tags), read from GitHub's API every 48
  hours. Pre-releases such as `v1.3.0-beta.1` count when "Also check beta release" is ticked, and nothing is asked
  when "Show update if available" isn't. `KUMA_UPDATE_CHECK_URL=off` stops it altogether.
- Changes made through the REST API reach open browsers only when they reload.

Sessions are a random token in an `HttpOnly`, `SameSite=Lax` cookie, `Secure` behind HTTPS (the proxy must
send `X-Forwarded-Proto`). Only a SHA-256 hash of the token is stored. A remembered login lasts 7 days and is
renewed while used, and changing the password ends every other login. Passwords are hashed with scrypt in
better-auth's format, so `kuma-import` copies the account from the Node.js backend unchanged. Logins are limited to 10 a minute per username and 60 a minute overall.

A new password, at setup or when it's changed, is checked against the
[Pwned Passwords](https://haveibeenpwned.com/Passwords) list of leaked passwords, as the Node.js backend did. Only
the first 5 characters of its SHA-1 hash are sent. If the list can't be reached, the password is refused, so a
server without internet access needs `KUMA_DISABLE_LEAK_PASSWORD_CHECK=true`.

**Two-factor login** is set up under **Settings > Security > 2FA Settings**, with any authenticator app: codes
are RFC 6238, 6 digits every 30 seconds, and a code from the step before or after also works. Turning it on ends
the account's other logins, which didn't need a code. With it on, a right password gives a challenge for 10
minutes rather than a session, and the code completes the login. A code works only once, a challenge allows 5
tries, and 10 wrong codes in a row refuse codes for 15 minutes. There are no backup codes, which the UI never
showed, so if the app is lost, turn it off in the database: `UPDATE app_user SET totp_enabled = false,
totp_secret = NULL`. The key is stored as it is, like the password hashes beside it.

**API keys** made under **Settings > API Keys** are the REST API's keys below, with the scopes ticked in the
form. Deleting one revokes it at once.

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

## HTTP options, proxies and domain expiry

HTTP, keyword and JSON query monitors take every option of the form:

- **Logins**: HTTP basic, bearer token, NTLM, mTLS with a client certificate and optionally the server's CA, and OAuth2
  client credentials, whose token is kept until it expires and asked for again once if the server answers 401.
  WebSocket monitors take all of them but NTLM. Headers the monitor sets replace the login's `Authorization` header.
- **Proxies**, added under **Settings > Proxies**: HTTP, HTTPS, SOCKS5 and SOCKS4, with an optional login. `socks` and
  `socks5h` resolve names on the proxy, and `socks5` and `socks4` here, as in Node.js. A proxy that's switched off isn't
  used. Applying a proxy to existing monitors sets it on every HTTP, keyword and JSON query monitor.
- **Cache busting** adds a random `uptime_kuma_cachebuster` parameter, and **IP family** connects only over IPv4 or
  IPv6.
- **Saved responses** keep the body of an unaccepted response, and optionally of accepted ones, for notification
  templates, as `heartbeatJSON.response`, cut to the length set. They're used by that beat's notifications and not stored.
- A JSON query monitor can be DOWN at once when its query fails, without retries.

**Domain expiry**: monitors with the option on, of the types whose target has a host name, look up when their domain
expires, at most once a day per domain, from the registry's RDAP server. IANA's list of RDAP servers is fetched once a
week, with a built-in copy for when it can't be reached. Alerts go to the monitor's notifications on the days set under
**Settings > Notifications**, 7, 14 and 21 by default, once per deadline until the domain is renewed. The monitor's page
shows the expiry date. Domains under a suffix that ICANN doesn't manage, such as `github.io`, and IP addresses can't be
tracked, which the form says.

## Globalping, GameDig and Tailscale ping

**Globalping** monitors run a ping, HTTP or DNS measurement from a probe of [Globalping](https://globalping.io) at the
monitor's location, such as `Germany` or `AWS+eu-west-1`, and judge it as the Node.js server did. Without a token
under **Settings > General**, the API's anonymous limits apply.

**GameDig** monitors query a game server with the game's own protocol, and are UP with its name. The protocols of 273
of [gamedig](https://github.com/gamedig/node-gamedig)'s 358 games are ported: Valve's A2S, GameSpy 1, 2 and 3, Quake 2
and 3, All-Seeing Eye, and Minecraft (Java, Bedrock and GameSpy 3). The form lists only those games.
`internal/check/gamedig-games.mjs` makes the list from gamedig's.

**Tailscale ping** monitors send one disco ping through `tailscaled`, as `tailscale ping --c 1` did, by asking its local
API instead of running the command. In a container, mount its socket:
`-v /var/run/tailscale/tailscaled.sock:/var/run/tailscale/tailscaled.sock`, or set `TAILSCALE_SOCKET`.

## Docker, system and browser monitors

**Docker container** monitors ask a Docker host, added under **Settings > Docker Hosts**, about one container. It's UP
when it's running and healthy, or running without a health check, and PENDING while it starts or restarts. A host is
the daemon's socket or a `tcp://` or `https://` URL. To use the host's socket, mount it and give the server its group:

```bash
docker run ... -v /var/run/docker.sock:/var/run/docker.sock --group-add "$(stat -c %g /var/run/docker.sock)" ...
```

This gives the server control of the host's Docker. For an `https://` host, put `ca.pem`, `cert.pem` and `key.pem` in a
directory named after the host, under the directory that `DOCKER_TLS_DIR_PATH` names, as with the Node.js server.

**System service** monitors ask systemd whether a unit is active, as `systemctl is-active` does, over D-Bus instead of
running `systemctl`. In a container, mount the host's system bus. Docker's default AppArmor profile blocks D-Bus, so the
container also needs `--security-opt apparmor=unconfined`, or a profile of its own that allows the calls to
`org.freedesktop.systemd1`:

```bash
docker run ... -v /run/dbus/system_bus_socket:/var/run/dbus/system_bus_socket --security-opt apparmor=unconfined ...
```

**PM2** monitors run `pm2 jlist`, as the Node.js server did, so they only work where the server runs next to PM2, not in
the image. **SFTP** monitors log in with a password or a private key, and check that the path exists, if one is set.
As in Node.js, the server's host key isn't checked. **gRPC keyword** monitors read the service from the `.proto` text,
call one unary method with the JSON body, and look for the keyword in the JSON response.

**Real browser** monitors load the page in Chrome, keep a screenshot of it, which the monitor's page shows, and are UP
for a status from 200 to 399. Use a remote browser, added under **Settings > Remote Browsers**: its URL is a Chrome
DevTools Protocol address, such as `ws://browserless:3000?token=...` for browserless, or `http://chrome:9222` for Chrome
started with `--remote-debugging-port`. The image has no Chrome of its own. Where the server runs next to one, it finds
`chromium` or `google-chrome`, or uses the path under **Settings > General**, which must be one of the usual paths unless
`UPTIME_KUMA_ALLOW_ALL_CHROME_EXEC=1` is set. As with Playwright, Chrome runs without its sandbox.

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

**Settings > Monitor History** decides how many days of raw beats are kept, as `RETENTION_DAYS` did, which now
only gives the first value, until the setting is saved. `0` keeps them forever. Beats are dropped a day at a time,
so up to a day more is kept. The hourly and daily statistics are kept regardless, so uptime and charts over longer
periods still work. The page also shows the database's size. There's no Shrink Database button, as PostgreSQL
frees the space itself. **Clear all Statistics** there, and **Clear Data > Heartbeats** on a monitor's page,
delete the beats, the statistics and the history imported from Node.js, of every monitor or of that one, and
restart the monitors. Search Engine Visibility answers `/robots.txt`, which discourages indexing until it's allowed.

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

| Variable                           | Default          | Purpose                                                                                         |
| ---------------------------------- | ---------------- | ----------------------------------------------------------------------------------------------- |
| `DATABASE_URL`                     | required         | PostgreSQL URL of a server with TimescaleDB Community Edition                                   |
| `ANALYTICS_API_TOKEN`              | required         | Admin bearer token for managing API keys, at least 32 characters                                |
| `LISTEN_ADDR`                      | `127.0.0.1:3002` | Address to listen on                                                                            |
| `RETENTION_DAYS`                   | `365`            | First value of the raw beat retention, which Settings > Monitor History then decides            |
| `CHECK_CONCURRENCY`                | `2000`           | Maximum checks running at once                                                                  |
| `API_KEY_RATE_LIMIT`               | `50`             | Requests per second allowed per API key                                                         |
| `KUMA_BASE_URL`                    | not set          | Public address of the web UI, for links in notifications. Without it, alerts have no link       |
| `KUMA_TIMEZONE`                    | `UTC`            | IANA timezone that notifications show, such as `Asia/Hong_Kong`                                 |
| `OTEL_EXPORTER_OTLP_ENDPOINT`      | not set          | OTLP gRPC endpoint. Without it nothing is exported. Other standard `OTEL_*` variables apply too |
| `KUMA_TRACE_SUCCESS_RATIO`         | `0.01`           | Share of successful traces to export. Failures are always exported                              |
| `GRAFANA_DB_PASSWORD`              | not set          | Creates or updates the read-only `kuma_grafana` database role                                   |
| `KUMA_INITIAL_ADMIN_USERNAME`      | not set          | With the password, creates the account at start if there's none. Ignored once there's one       |
| `KUMA_INITIAL_ADMIN_PASSWORD`      | not set          | The password of that account, which must pass the same checks as at setup                       |
| `KUMA_DISABLE_LEAK_PASSWORD_CHECK` | `false`          | Stops checking new passwords against Pwned Passwords, for servers without internet access       |
| `KUMA_UPDATE_CHECK_URL`            | GitHub's tags    | Where the update check lists releases, or `off`                                                 |
| `KUMA_GITHUB_CLIENT_ID`            | not set          | Turns on logging in with GitHub, with this OAuth App                                            |
| `KUMA_GITHUB_CLIENT_SECRET`        | not set          | The OAuth App's secret                                                                          |
| `KUMA_GITHUB_ORG`                  | not set          | The organization whose teams decide access                                                      |
| `KUMA_GITHUB_ADMIN_TEAMS`          | not set          | Team slugs whose members are admins, comma-separated                                            |
| `KUMA_GITHUB_VIEWER_TEAMS`         | not set          | Team slugs whose members are viewers, or `*` for every member of the organization               |
| `KUMA_PASSWORD_LOGIN`              | `true`           | `false` refuses logging in with a password, once GitHub login works                             |
| `KUMA_SECRET_KEY`                  | not set          | 32 bytes in base64 or hex that encrypt stored secrets. See "Encrypting stored secrets"          |
| `KUMA_SECRET_KEY_OLD`              | not set          | Earlier keys, comma-separated, while rotating. Both also come from a file with `_FILE`          |
| `CLOUDFLARE_TUNNEL_TOKEN`          | not set          | In `compose.yaml`, the token of the Cloudflare Tunnel that the `tunnel` profile runs            |
| `DOCKER_TLS_DIR_PATH`              | not set          | Client certificates for `https://` Docker hosts, in a directory per host                        |

To deploy on Kubernetes, use the Helm chart in [`charts/uptime-kuma`](charts/uptime-kuma/README.md), which
runs the same stack.

### Encrypting stored secrets

With `KUMA_SECRET_KEY`, these are stored encrypted, so that a copy of the database, such as a backup, doesn't give
them away: authenticator keys, GitHub tokens, notification settings, proxy passwords and the web push (VAPID)
private key. Each is encrypted with AES-256-GCM for its column and row, so that a value copied to another row
doesn't open. Monitors' credentials aren't: they're spread over many columns that checks, the API and the UI read,
and the Grafana role can't read them already.

```bash
openssl rand -base64 32   # keep it in a secret manager or a file, never in the database
```

- **Turning it on:** set `KUMA_SECRET_KEY` (or `KUMA_SECRET_KEY_FILE`, a path to the key), and restart. Plain values
  are encrypted at start, and the log says how many. Give `kuma-import` the same key.
- **Losing it** loses those values: the server won't start with encrypted values and no key, or the wrong key,
  rather than run without them. Back the key up apart from the database.
- **Rotating it:** set the new key as `KUMA_SECRET_KEY` and the old one as `KUMA_SECRET_KEY_OLD`, restart, then run
  `kuma-analytics rekey` with the same variables, which encrypts everything again with the new key. Then remove
  `KUMA_SECRET_KEY_OLD`.
- **Turning it off** isn't supported: the values would have to be decrypted first, and nothing does that.

Run a single instance: there's no coordination between schedulers, so two instances
would both run every check.

The service speaks plain HTTP, so keep it on a private network or behind a TLS proxy. Browsers
can't set headers on `EventSource`, so `/api/v1/events` is meant for a proxy or server-side
client until login moves over.

### The apprise image

The image holds only the two programs. The `apprise` notification provider runs the Python `apprise` command, so
it's in a second image, the Dockerfile's `apprise` target, published with an `-apprise` suffix on every tag, such
as `ghcr.io/pantheon-lab/uptime-kuma:1.2.3-apprise`, and about 120 MB larger. Use it only for apprise. In the
Helm chart, set `image.apprise=true`; to build it, `docker build --target apprise .`.

### Cloudflare Tunnel

This service doesn't start `cloudflared` itself, as Uptime Kuma did. Cloudflare's own `cloudflared` runs beside it
instead, as Cloudflare recommends for containers, and needs no port open:

1. In Cloudflare's dashboard, under Networks > Tunnels, create a tunnel and copy its token.
2. Add a public hostname to it, for the service `http://analytics:3002` with Docker Compose, or
   `http://<release>-uptime-kuma-analytics:3002` with the Helm chart.
3. With Docker Compose, set `CLOUDFLARE_TUNNEL_TOKEN` and run `docker compose --profile tunnel up -d`. With the Helm
   chart, set `cloudflared.enabled=true` and `cloudflared.token`, or `cloudflared.existingSecret`.

Then set Trust Proxy under Settings > Reverse Proxy, and `KUMA_BASE_URL` to the public hostname.

### Behind a reverse proxy

The web UI gets its live updates as Server-Sent Events from `GET /api/realtime`, a response that stays open, and
sends everything else as ordinary requests. It doesn't use WebSockets, so no `Upgrade` headers are needed. The
proxy must pass the stream on as it comes instead of holding it back: the service sends `X-Accel-Buffering: no`,
which nginx follows, and Caddy and Traefik pass event streams on at once already. The service writes to the stream
at least every 25 seconds, so a read timeout of a minute or more is enough. With nginx:

```nginx
location / {
    proxy_pass http://127.0.0.1:3002;
    proxy_http_version 1.1;
    proxy_set_header Host $http_host;
    proxy_set_header X-Forwarded-Host $http_host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_read_timeout 120s;
}
```

Pass the `Host` header on as the browser sent it, with its port, as `$http_host` does: logging in and changes are
refused when the browser's `Origin` doesn't match it, and nginx's `$host` leaves the port out.

Then set Trust Proxy under Settings > Reverse Proxy, so that status pages' domains and links use
`X-Forwarded-Host` and `X-Forwarded-Proto`, and `KUMA_BASE_URL` to the public address. The login cookie is marked
`Secure` when `X-Forwarded-Proto` is `https`, with or without Trust Proxy.

## API

The API is described by an OpenAPI document at `GET /api/v1/openapi.json`, which you can load
into Swagger UI, Postman or a client generator.

### MCP

`/mcp` is a [Model Context Protocol](https://modelcontextprotocol.io) server over Streamable HTTP, for AI
assistants. Its tools are the API's read-only operations, such as `listMonitors`, `getMonitor`, `getBeats` and
`getStats`, with their parameters. It needs an API key, like the API, and each tool asks the API with that key, so
the key's scopes and rate limit apply: give the assistant a key with only the `:read` scopes it needs. It can't
change anything.

```json
{
  "mcpServers": {
    "uptime-kuma": {
      "url": "https://kuma.example.com/mcp",
      "headers": { "Authorization": "Bearer kuma_..." }
    }
  }
}
```

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

- `Webpush` signs with this server's VAPID key pair, made the first time the form asks for it, and never sent to
  browsers. `kuma-import` copies Uptime Kuma's pair, so that browsers subscribed there keep getting messages.
- `nostr` sends a NIP-17 private message, gift-wrapped with NIP-59, to each recipient on each relay, and logs in to
  relays that ask (NIP-42), as Uptime Kuma did.
- `apprise` runs the `apprise` command, which is only in the `-apprise` image.
- `NOTIFICATION_PROXY` isn't supported.
- `AlertNow`, `SIGNL4` and `SMSManager` fix bugs: a crash on recovery, an alert ID of `UptimeKuma-undefined`, and
  `gateway=undefined`.

## Importing from MariaDB

`kuma-import` copies monitors, groups, notifications, history, the account, status pages, maintenance windows,
settings, API keys and alert state from an Uptime Kuma MariaDB database, and `-verify` checks the result. It only
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
  because checking without them would show false results.
- `proxies`, `dockerHosts` and `remoteBrowsers` count the proxies, Docker hosts and remote browsers copied. Monitors
  keep using them. An imported proxy isn't the default, so that the default chosen here stays.
- `tags` and `tagLinks` count the tags copied and how many were put on monitors. Monitors that were already resumed here keep theirs.
- `droppedSettings` would count settings that are lost but don't change whether a check passes. Every setting is
  imported now, so it's empty.
- `notifications.skipped` lists notifications that weren't imported, and `notifications.monitorsLosingAlerts`
  counts imported monitors that used one of them. Those monitors won't send that alert here.

You can run it as often as you like. It duplicates nothing and picks up changes, except for monitors that have
been resumed here, which are left alone because they produce their own data. The same goes for notifications
that have been switched on here.

**Settings** that take effect here are copied: `primaryBaseURL`, the entry page, search engine visibility, trusting
a proxy, the certificate and domain expiry days, the retention, the Steam key, the Globalping token, the Chrome
executable, and the update check's. One changed here since is left alone: the server records whether each value is
a default, the import's, or the UI's. `settings` in the report lists each with `copied`, `same`, `kept` and why, or
`not set in the source`, and never a value. A new retention applies at the next restart.

**API keys** keep working, so that scripts using them needn't change. better-auth's keys are stored as the SHA-256
of the key, as this service's are, and the older `uk<id>_…` keys with their bcrypt hash, which is checked once per
key and process. They get every scope, since Uptime Kuma gave them none, and keep their names, expiry and whether
they're on. A key revoked here stays revoked. They're used as `Authorization: Bearer <key>`, as Uptime Kuma took
them.

**Alert state** comes along, so that alerts aren't sent again: the certificate expiry alerts already sent, the
certificates last seen, which the monitor pages then show before the first check, and the domains' expiry and last
alert, unless checked here since.

**Skipped monitors' history** (their beats and statistics) is kept apart, by their id in the source, and given to
the monitor when a later version can import it, even after Uptime Kuma has pruned it. `skippedHistory` counts it.

**The account** is copied with its password, when there's no account here yet, so it can't take over a server
that's been set up. Logging in works as before, with the same password and authenticator app. If two-factor login
is on, the importer needs the key that encrypted the authenticator key: set `SOURCE_AUTH_SECRET` to Uptime Kuma's
`UPTIME_KUMA_AUTH_SECRET`, or to `authSecret` in `db-config.json` in its data directory. Without it the account
isn't imported, because the password alone would then log in. An account from before better-auth, from a server
nobody has logged in to since, has a bcrypt hash, which the first login here replaces.

**Status pages** come with their groups, monitors, incidents and custom domains, and **maintenance windows** with
their monitors and status pages. Monitors that weren't imported are left out, and counted in the report. A window
set to the server's timezone gets Uptime Kuma's server timezone, when it was set, so that it stays at the same time. Uploaded logos
are files in Uptime Kuma's data directory, so mount it and set `SOURCE_DATA_DIR` to it to copy them: without it,
pages show the default logo, and `statusPages.logosMissing` counts them.

```bash
docker compose run --rm --no-deps -e SOURCE_MARIADB_DSN -e SOURCE_AUTH_SECRET -e SOURCE_DATA_DIR=/source-data \
  -v /path/to/uptime-kuma/data:/source-data:ro --entrypoint /kuma-import analytics -apply
```

Each status page and window is copied once. Running the import again leaves those copied before alone, so changes
made here are kept; delete one here to copy it again. A page whose slug is already used here is skipped, and a
custom domain another page here shows stays with that page.

Imported notifications stay off until `-activate`, even for monitors you resume by hand, so that a parallel run
doesn't send every alert twice. `-activate` is therefore also when alerts start, and should come when you stop
Uptime Kuma's alerts. Check them with `POST /api/v1/notifications/{id}/test`, which works while they're off.

Uptime before the last whole hour comes from the source's aggregates, and everything after comes from beats, so
nothing counts twice. The same applies per day for daily figures. Dashboards and the API read the `uptime_hourly`
and `uptime_daily` views, which join the two.

### Checking the move

`-verify` compares each imported monitor with the source, and imports nothing: that each of the source's beats
is here (beats this service recorded itself don't count against it), the newest beat, and the uptime over the last 24 whole hours, 30 whole days and 365 whole days
before the source's newest beat, from the source's statistics and this service's views. Uptime may differ by
0.01 percentage points. It prints a JSON report that lists every monitor that differs and how, and exits with 3 if
any does, 1 if it couldn't check, and 0 otherwise.

```bash
docker compose run --rm --no-deps -e SOURCE_MARIADB_DSN --entrypoint /kuma-import analytics -verify
```

Run it right after an import: while Uptime Kuma keeps checking, the source moves ahead of what was imported.

### Cutover runbook

1. **Dry run.** Read the report: skipped monitors, notifications and API keys, and settings that would be kept.
   Fix what you can in Uptime Kuma, or accept it.
2. **Import while both run.** `-apply`. Monitors arrive paused and notifications off, so nothing checks or alerts
   twice. Set `KUMA_SECRET_KEY` first if secrets are to be encrypted. Look around the UI, and test notifications
   with `POST /api/v1/notifications/{id}/test`.
3. **Verify.** `-apply` again, then `-verify` straight after. It should exit 0; read any differences.
4. **Stop Uptime Kuma**, so that its beats, statistics and alerts stop, and nothing changes in the source.
5. **Last import.** `-apply -activate`: it copies the last beats, then resumes the monitors that were active and
   switches on their notifications.
6. **Restart** this service (`docker compose restart analytics`), which starts the monitors and applies an
   imported retention.
7. **Verify again.** `-verify` must exit 0, since the source no longer changes.
8. **Point traffic here**: DNS, the reverse proxy, push monitors' URLs, and status page domains.

**Going back**, if something's wrong before step 8: start Uptime Kuma again, which still has everything, since the
import only reads it. Then pause the monitors here, with `POST /api/v1/monitors/{id}/pause` or the UI, so that
they don't alert alongside it. After step 8, point traffic back first. Beats recorded here in between stay here,
and aren't copied back.

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
`TEST_RADIUS`, which `internal/check/network_test.go` describes. `TEST_DOCKER_CONTAINER` names a running container to ask
the local Docker daemon about, and `TEST_CHROME` is a Chrome or Chromium executable for the real browser monitor. The
system service test uses this machine's systemd, if it has one. `TEST_MINECRAFT` is a Minecraft server's host:port,
`TEST_TAILSCALE_PEER` a peer to ping through this machine's tailscaled, `TEST_GLOBALPING` runs one ping from the
real Globalping API, and `TEST_NOSTR_RELAY` is the `ws://` address of a relay, such as `scsibug/nostr-rs-relay` in
Docker, which the nostr provider sends to and reads back from.

`internal/e2e/browser/` has scripts that drive the web UI in a browser against the built image. Each says what it needs.

## Licensing

Continuous aggregates, compression, retention policies and other features this service
uses need TimescaleDB Community Edition, which is free to self-host but
[can't be sold as a service](https://www.tigerdata.com/docs/about/latest/timescaledb-editions).
Check this before offering it as a hosted product.
