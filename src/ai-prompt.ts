/**
 * A prompt, in Markdown, that hands a failing monitor to an AI agent to trace: what the monitor
 * checks, what it saw, and commands to reproduce the check.
 *
 * Only an allowlist of the monitor's fields goes in. Credentials, headers' values, request bodies,
 * connection strings and push tokens never do, and addresses and messages are redacted.
 */

const DOWN = 0;
const UP = 1;
const PENDING = 2;
const MAINTENANCE = 3;

export interface PromptMonitor {
    id: number;
    name: string;
    pathName?: string;
    type: string;
    description?: string | null;
    url?: string | null;
    method?: string | null;
    hostname?: string | null;
    port?: number | null;
    keyword?: string | null;
    invertKeyword?: boolean;
    jsonPath?: string | null;
    jsonPathOperator?: string | null;
    expectedValue?: string | null;
    accepted_statuscodes?: string[] | null;
    interval?: number;
    retryInterval?: number;
    maxretries?: number;
    timeout?: number;
    ignoreTls?: boolean;
    upsideDown?: boolean;
    maxredirects?: number;
    proxyId?: number | null;
    headers?: string | null;
    authMethod?: string | null;
    dns_resolve_type?: string | null;
    dns_resolve_server?: string | null;
    docker_container?: string | null;
    databaseConnectionString?: string | null;
    grpcUrl?: string | null;
    grpcServiceName?: string | null;
    grpcMethod?: string | null;
    grpcEnableTls?: boolean;
    mqttTopic?: string | null;
    game?: string | null;
    active?: boolean | number;
    tags?: { name: string; value?: string | null }[];
}

export interface PromptBeat {
    status: number;
    /** "2026-11-08 01:26:34.123", in UTC, as the server sends it */
    time: string;
    msg?: string | null;
    ping?: number | null;
    traceId?: string | null;
}

export interface PromptCertificate {
    validTo?: string;
    daysRemaining?: number;
    issuer?: { CN?: string; O?: string } | null;
}

export interface PromptInput {
    monitor: PromptMonitor;
    /** Recent checks, in any order */
    beats: PromptBeat[];
    /** Recent status changes, in any order */
    events: PromptBeat[];
    uptime24?: number | null;
    uptime30d?: number | null;
    certificate?: PromptCertificate | null;
    /** For a group, the monitors in it that aren't up */
    failingChildren?: { name: string; status: number; msg?: string | null }[];
}

export interface PromptOptions {
    now: Date;
    /** The app's address, for links */
    baseURL?: string | null;
    /** How many recent checks to list */
    beatCount?: number;
}

const SENSITIVE_PARAM = /token|key|secret|pass|auth|sig|session|code|credential|jwt/i;
const MAX_MESSAGE = 400;

/**
 * An address without its user name, password, or sensitive query values
 * @param {string} text Address
 * @returns {string} Redacted address
 */
export function redactURL(text: string): string {
    try {
        const url = new URL(text);
        if (url.username || url.password) {
            url.username = url.username ? "REDACTED" : "";
            url.password = "";
        }
        for (const name of [...url.searchParams.keys()]) {
            if (SENSITIVE_PARAM.test(name)) {
                url.searchParams.set(name, "REDACTED");
            }
        }
        return url.toString();
    } catch {
        return redactText(text);
    }
}

/**
 * A message without what looks like credentials: passwords in addresses, sensitive query values
 * and bearer tokens
 * @param {string} text Message
 * @returns {string} Redacted message
 */
export function redactText(text: string): string {
    return text
        .replace(/([a-z][a-z0-9+.-]*:\/\/)[^/\s:@]+(:[^/\s@]*)?@/gi, "$1REDACTED@")
        .replace(
            /([?&][^=&\s]*(?:token|key|secret|pass|auth|sig|session|code|credential|jwt)[^=&\s]*=)[^&\s]+/gi,
            "$1REDACTED"
        )
        .replace(/(bearer\s+)[a-z0-9._~+/=-]{8,}/gi, "$1REDACTED");
}

/**
 * An address for a command: without the user name and password, which the command says how to add
 * @param {string} text Address
 * @returns {string} Address
 */
function commandURL(text: string): string {
    try {
        const url = new URL(text);
        url.username = "";
        url.password = "";
        return redactURL(url.toString());
    } catch {
        return redactText(text);
    }
}

/**
 * @param {string} host Host
 * @returns {boolean} Whether it's an IP address, which needs no name lookup
 */
function isIPAddress(host: string): boolean {
    return /^\d{1,3}(\.\d{1,3}){3}$/.test(host) || host.includes(":") || host.startsWith("[");
}

/**
 * @param {string} text Text
 * @returns {string} The text quoted for a POSIX shell
 */
function shellQuote(text: string): string {
    return `'${text.replace(/'/g, "'\\''")}'`;
}

/**
 * @param {string} time Server time, in UTC
 * @returns {Date} The time
 */
function parseTime(time: string): Date {
    return new Date(time.includes("T") ? time : time.replace(" ", "T") + "Z");
}

/**
 * @param {string} time Server time
 * @returns {string} "2026-11-08 01:26:34 UTC"
 */
function formatTime(time: string): string {
    const date = parseTime(time);
    if (Number.isNaN(date.getTime())) {
        return time;
    }
    return date
        .toISOString()
        .replace("T", " ")
        .replace(/\.\d+Z$/, " UTC");
}

/**
 * @param {number} ms Milliseconds
 * @returns {string} "3 h 12 min"
 */
function formatDuration(ms: number): string {
    const minutes = Math.max(0, Math.floor(ms / 60000));
    if (minutes < 1) {
        return "less than a minute";
    }
    const days = Math.floor(minutes / 1440);
    const hours = Math.floor((minutes % 1440) / 60);
    const mins = minutes % 60;
    return [days && `${days} d`, hours && `${hours} h`, mins && `${mins} min`].filter(Boolean).join(" ");
}

/**
 * @param {number} status Beat status
 * @returns {string} Its name
 */
export function statusName(status: number): string {
    return { [DOWN]: "DOWN", [UP]: "UP", [PENDING]: "PENDING", [MAINTENANCE]: "MAINTENANCE" }[status] ?? "UNKNOWN";
}

/**
 * @param {string|null|undefined} msg Message
 * @returns {string} The message, redacted, on one line and shortened
 */
function cleanMessage(msg: string | null | undefined): string {
    const text = redactText((msg ?? "").replace(/\s+/g, " ").trim());
    return text.length > MAX_MESSAGE ? text.slice(0, MAX_MESSAGE) + "…" : text;
}

/**
 * A host and port from a database connection string, without anything else in it
 * @param {string} text Connection string
 * @returns {{host: string, port: string}|null} Where it connects
 */
function connectionTarget(text: string): { host: string; port: string } | null {
    try {
        const url = new URL(text);
        if (url.hostname) {
            return { host: url.hostname, port: url.port };
        }
    } catch {
        // Not a URL: look for key=value pairs
    }
    const host = text.match(/(?:^|[;\s])(?:host|server|data source)\s*=\s*([^;,\s]+)(?:,(\d+))?/i);
    if (host) {
        const port = text.match(/(?:^|[;\s])port\s*=\s*(\d+)/i);
        return { host: host[1].replace(/^tcp:/i, ""), port: host[2] ?? port?.[1] ?? "" };
    }
    return null;
}

const DEFAULT_PORTS: Record<string, string> = {
    postgres: "5432",
    mysql: "3306",
    sqlserver: "1433",
    mongodb: "27017",
    redis: "6379",
};

/**
 * Commands that check what the monitor checks, from another machine
 * @param {PromptMonitor} m Monitor
 * @returns {string[]} Shell commands
 */
export function reproduceCommands(m: PromptMonitor): string[] {
    const commands: string[] = [];
    const timeout = Math.max(1, Math.round(m.timeout || 30));
    const hostChecks = (host: string) => {
        if (!isIPAddress(host)) {
            commands.push(`dig +short ${shellQuote(host)}`);
        }
    };

    if (["http", "keyword", "json-query", "real-browser", "websocket-upgrade"].includes(m.type) && m.url) {
        const url = commandURL(m.url);
        const method = shellQuote((m.method || "GET").toUpperCase());
        const insecure = m.ignoreTls ? " -k" : "";
        commands.push(
            `curl -sS -o /dev/null -w 'status %{http_code}, dns %{time_namelookup}s, connect %{time_connect}s, tls %{time_appconnect}s, total %{time_total}s\\n' -X ${method}${insecure} --max-time ${timeout} ${shellQuote(url)}`
        );
        // The response's headers and body often say why, such as which dependency is down
        commands.push(`curl -sSv${insecure} -X ${method} --max-time ${timeout} ${shellQuote(url)} 2>&1 | head -n 80`);
        try {
            const parsed = new URL(m.url);
            hostChecks(parsed.hostname);
            if (parsed.protocol === "https:" || parsed.protocol === "wss:") {
                const port = parsed.port || "443";
                commands.push(
                    `openssl s_client -connect ${shellQuote(`${parsed.hostname}:${port}`)} -servername ${shellQuote(parsed.hostname)} </dev/null 2>/dev/null | openssl x509 -noout -subject -issuer -dates`
                );
            }
        } catch {
            // An address that doesn't parse gets the curl commands only
        }
        if (url.includes("REDACTED")) {
            commands.push("# Put the real values where the address says REDACTED.");
        }
        if (m.authMethod && m.authMethod !== "none") {
            commands.push(
                `# The monitor logs in (${m.authMethod}): add the same to the curl commands, such as -u for basic.`
            );
        } else if (/^[a-z][a-z0-9+.-]*:\/\/[^/\s]*@/i.test(m.url)) {
            commands.push("# The address has a user name and password: add them with curl's -u.");
        }
        if (m.headers) {
            commands.push("# The monitor sends headers too: add them, with their values, with curl's -H.");
        }
        if (m.proxyId) {
            commands.push("# The monitor goes through a proxy: add it with curl's --proxy to compare.");
        }
    } else if (m.type === "port" && m.hostname) {
        commands.push(`nc -vz -w ${timeout} ${shellQuote(m.hostname)} ${m.port ?? ""}`.trim());
        hostChecks(m.hostname);
    } else if (m.type === "ping" && m.hostname) {
        commands.push(`ping -c 4 ${shellQuote(m.hostname)}`);
        commands.push(`traceroute ${shellQuote(m.hostname)}`);
        hostChecks(m.hostname);
    } else if (m.type === "dns" && m.hostname) {
        // An imported monitor's resolver and record type aren't checked, so they're quoted too
        const server = m.dns_resolve_server ? ` @${shellQuote(m.dns_resolve_server)}` : "";
        const recordType = shellQuote(m.dns_resolve_type || "A");
        commands.push(`dig${server} ${shellQuote(m.hostname)} ${recordType}`);
        commands.push(`dig +trace ${shellQuote(m.hostname)} ${recordType}`);
    } else if (m.type === "docker" && m.docker_container) {
        commands.push("# On the Docker host the monitor uses:");
        commands.push(`docker ps -a --filter name=${shellQuote(m.docker_container)}`);
        commands.push(
            `docker inspect --format '{{.State.Status}} {{.State.Health}}' ${shellQuote(m.docker_container)}`
        );
        commands.push(`docker logs --tail 100 ${shellQuote(m.docker_container)}`);
    } else if (m.type === "grpc-keyword" && m.grpcUrl) {
        const target = redactText(m.grpcUrl);
        // Without TLS, as the monitor connects then, grpcurl needs -plaintext
        const flags = m.grpcEnableTls ? "" : "-plaintext ";
        commands.push(`grpcurl ${flags}${shellQuote(target)} list`);
        if (m.grpcServiceName && m.grpcMethod) {
            commands.push(
                `grpcurl ${flags}${shellQuote(target)} ${shellQuote(`${m.grpcServiceName}/${m.grpcMethod}`)}`
            );
        }
        commands.push(
            "# list needs the server to offer reflection; without it, give grpcurl the monitor's .proto with -proto."
        );
    } else if (m.databaseConnectionString) {
        const target = connectionTarget(m.databaseConnectionString);
        if (target) {
            const port = target.port || DEFAULT_PORTS[m.type] || "";
            commands.push(`nc -vz -w ${timeout} ${shellQuote(target.host)} ${port}`.trim());
            hostChecks(target.host);
            commands.push("# Then connect with the database's own client and run the monitor's query, if it has one.");
        }
    } else if (m.hostname) {
        if (m.port) {
            commands.push(`nc -vz -w ${timeout} ${shellQuote(m.hostname)} ${m.port}`);
        }
        hostChecks(m.hostname);
    }
    return commands;
}

/**
 * The monitor's settings that bear on its checks, as list items
 * @param {PromptMonitor} m Monitor
 * @returns {string[]} Lines
 */
function settingLines(m: PromptMonitor): string[] {
    const lines: string[] = [];
    const add = (label: string, value: unknown) => {
        if (value !== undefined && value !== null && value !== "") {
            lines.push(`- ${label}: ${value}`);
        }
    };
    add("Name", m.pathName || m.name);
    add("Type", m.type);
    add("Description", m.description ? cleanMessage(m.description) : null);
    add("Address", m.url ? redactURL(m.url) : null);
    if (m.hostname && !m.url) {
        add("Host", m.port ? `${m.hostname}:${m.port}` : m.hostname);
    }
    if (m.databaseConnectionString) {
        const target = connectionTarget(m.databaseConnectionString);
        add(
            "Database",
            target
                ? `${target.host}${target.port ? ":" + target.port : ""} (connection string withheld)`
                : "(connection string withheld)"
        );
    }
    add("gRPC", m.grpcUrl ? `${redactText(m.grpcUrl)} ${m.grpcServiceName ?? ""}/${m.grpcMethod ?? ""}` : null);
    add("MQTT topic", m.mqttTopic);
    add("Container", m.docker_container);
    add("Game", m.game);
    if (["http", "keyword", "json-query"].includes(m.type)) {
        add("Method", (m.method || "GET").toUpperCase());
        add("Accepted status codes", m.accepted_statuscodes?.join(", "));
        add("Maximum redirects", m.maxredirects);
    }
    if (m.type === "keyword" && m.keyword) {
        add(m.invertKeyword ? "Must not contain" : "Must contain", JSON.stringify(m.keyword));
    }
    if (m.type === "json-query" && m.jsonPath) {
        add("JSON query", `${m.jsonPath} ${m.jsonPathOperator || "=="} ${JSON.stringify(m.expectedValue ?? "")}`);
    }
    if (m.type === "dns") {
        add("Record", m.dns_resolve_type);
        add("Resolver", m.dns_resolve_server);
    }
    if (m.headers) {
        try {
            const names = Object.keys(JSON.parse(m.headers));
            add("Headers sent", names.length ? `${names.join(", ")} (values withheld)` : null);
        } catch {
            add("Headers sent", "(withheld)");
        }
    }
    add("Logs in with", m.authMethod && m.authMethod !== "none" ? `${m.authMethod} (credentials withheld)` : null);
    add("Through a proxy", m.proxyId ? "yes" : null);
    add("TLS errors ignored", m.ignoreTls ? "yes" : null);
    if (m.upsideDown) {
        lines.push("- Upside down mode: on. The monitor is UP when the check fails, and DOWN when it succeeds.");
    }
    if (m.interval) {
        const retries = m.maxretries ?? 0;
        add(
            "Checked every",
            retries > 0
                ? `${m.interval}s, and after a failure every ${m.retryInterval || m.interval}s, up to ${retries} more times before DOWN`
                : `${m.interval}s, DOWN at the first failure`
        );
    }
    add("Timeout", m.timeout ? `${m.timeout}s` : null);
    add("Tags", m.tags?.length ? m.tags.map((t) => (t.value ? `${t.name}: ${t.value}` : t.name)).join(", ") : null);
    return lines;
}

/**
 * When the monitor went down, from its status changes: the newest change, when it's to DOWN
 * @param {PromptBeat[]} events Status changes
 * @returns {PromptBeat|null} That change
 */
function downSince(events: PromptBeat[]): PromptBeat | null {
    const newest = [...events].sort((a, b) => parseTime(b.time).getTime() - parseTime(a.time).getTime())[0];
    return newest && newest.status === DOWN ? newest : null;
}

/**
 * The monitor's section: its settings, state, checks and commands
 * @param {PromptInput} input The monitor and what it saw
 * @param {PromptOptions} options Options
 * @param {number} level Heading level
 * @returns {string} Markdown
 */
function monitorSection(input: PromptInput, options: PromptOptions, level: number): string {
    const h = "#".repeat(level);
    const m = input.monitor;
    const beats = [...input.beats].sort((a, b) => parseTime(b.time).getTime() - parseTime(a.time).getTime());
    const latest = beats[0];
    const out: string[] = [];

    out.push(`${h} The monitor`, "", ...settingLines(m));
    if (options.baseURL) {
        out.push(`- In Uptime Kuma: ${options.baseURL.replace(/\/$/, "")}/dashboard/${m.id}`);
    }

    out.push("", `${h} What it sees`, "");
    const since = downSince(input.events);
    const status = latest ? statusName(latest.status) : "no checks yet";
    if (m.active === false || m.active === 0) {
        out.push("- Status: paused");
    } else if (since) {
        const duration = formatDuration(options.now.getTime() - parseTime(since.time).getTime());
        out.push(`- Status: ${status}, DOWN since ${formatTime(since.time)} (${duration})`);
    } else {
        out.push(`- Status: ${status}`);
    }
    if (latest?.msg) {
        out.push(`- Latest message: ${cleanMessage(latest.msg)}`);
    }
    // An all-zero id comes from a check that wasn't traced
    const traced = beats.find((b) => b.status === DOWN && b.traceId && !/^0+$/.test(b.traceId));
    if (traced) {
        out.push(
            `- Trace ID of the latest failed check: \`${traced.traceId}\`. If Uptime Kuma exports traces (\`OTEL_EXPORTER_OTLP_ENDPOINT\` is set), the check's OpenTelemetry trace, with its timings and error, is in the trace backend, such as Tempo. If it isn't found there, they aren't exported: go on without it.`
        );
    }
    const pings = beats.filter((b) => b.status === UP && typeof b.ping === "number").map((b) => b.ping as number);
    if (pings.length) {
        const avg = Math.round(pings.reduce((a, b) => a + b, 0) / pings.length);
        out.push(
            `- Response time of the recent successful checks: ${avg} ms on average, ${Math.max(...pings)} ms at most`
        );
    }
    if (typeof input.uptime24 === "number") {
        out.push(
            `- Uptime: ${(input.uptime24 * 100).toFixed(2)}% over 24 hours` +
                (typeof input.uptime30d === "number" ? `, ${(input.uptime30d * 100).toFixed(2)}% over 30 days` : "")
        );
    }
    if (input.certificate?.validTo) {
        const issuer = input.certificate.issuer?.O || input.certificate.issuer?.CN;
        out.push(
            `- TLS certificate: valid to ${input.certificate.validTo} (${input.certificate.daysRemaining} days left)${issuer ? `, issued by ${issuer}` : ""}`
        );
    }

    if (input.failingChildren?.length) {
        out.push("", `${h} Monitors in this group that aren't up`, "");
        for (const child of input.failingChildren) {
            out.push(`- ${child.name}: ${statusName(child.status)}${child.msg ? ` (${cleanMessage(child.msg)})` : ""}`);
        }
    }

    const shown = beats.slice(0, options.beatCount ?? 20);
    if (shown.length) {
        out.push(
            "",
            `${h} Recent checks, newest first`,
            "",
            "| Time | Status | Response time | Message |",
            "| --- | --- | --- | --- |"
        );
        for (const b of shown) {
            const ping = typeof b.ping === "number" ? `${b.ping} ms` : "";
            out.push(
                `| ${formatTime(b.time)} | ${statusName(b.status)} | ${ping} | ${cleanMessage(b.msg).replace(/\|/g, "\\|")} |`
            );
        }
    }

    const events = [...input.events]
        .sort((a, b) => parseTime(b.time).getTime() - parseTime(a.time).getTime())
        .slice(0, 10);
    if (events.length) {
        out.push("", `${h} Recent status changes, newest first`, "");
        for (const e of events) {
            out.push(`- ${formatTime(e.time)}: ${statusName(e.status)}${e.msg ? `, ${cleanMessage(e.msg)}` : ""}`);
        }
    }

    const commands = reproduceCommands(m);
    if (commands.length) {
        out.push("", `${h} Commands to reproduce the check`, "", "```sh", ...commands, "```");
    }
    if (m.type === "push") {
        out.push(
            "",
            "This is a push monitor: it doesn't check anything itself, but expects a job or service to call its push address at least every interval. DOWN means the calls stopped, so look at whatever sends them."
        );
    }
    return out.join("\n");
}

const INSTRUCTIONS = `## What I need from you

1. From the evidence below, work out what is most likely failing: name resolution, the network path, TLS, the service itself, something it depends on, or the monitor's own settings.
2. Reproduce the check from a machine you can use, with the commands given or better ones, and say what each result shows.
3. If you can reach the service's code, logs, metrics, traces or infrastructure, follow the failure there, starting from when it began.
4. Tell me the root cause, how sure you are, the fix, and how to confirm it worked.

Ask me before changing anything. Secrets such as passwords, tokens and header values were left out on purpose: ask me if you need one.`;

/**
 * A prompt for one failing monitor
 * @param {PromptInput} input The monitor and what it saw
 * @param {PromptOptions} options Options
 * @returns {string} Markdown
 */
export function buildMonitorPrompt(input: PromptInput, options: PromptOptions): string {
    const name = input.monitor.pathName || input.monitor.name;
    return [
        `# Investigate a failing monitor: ${name}`,
        "",
        `Uptime Kuma, which watches our services, reports a problem with the monitor below. Help me find out why. The time now is ${options.now
            .toISOString()
            .replace("T", " ")
            .replace(/\.\d+Z$/, " UTC")}.`,
        "",
        INSTRUCTIONS,
        "",
        monitorSection(input, options, 2),
        "",
    ].join("\n");
}

/**
 * A prompt for several failing monitors at once, asking first whether they share a cause
 * @param {PromptInput[]} inputs The monitors and what they saw
 * @param {PromptOptions} options Options
 * @returns {string} Markdown
 */
export function buildIncidentPrompt(inputs: PromptInput[], options: PromptOptions): string {
    if (inputs.length === 1) {
        return buildMonitorPrompt(inputs[0], options);
    }
    const sections = inputs.map(
        (input) => `## ${input.monitor.pathName || input.monitor.name}\n\n${monitorSection(input, options, 3)}`
    );
    return [
        `# Investigate ${inputs.length} failing monitors`,
        "",
        `Uptime Kuma, which watches our services, reports ${inputs.length} monitors failing. Help me find out why. The time now is ${options.now
            .toISOString()
            .replace("T", " ")
            .replace(/\.\d+Z$/, " UTC")}.`,
        "",
        "First, say whether they share a cause: the same host, network, DNS, provider, dependency or time they started. Then treat each cause on its own.",
        "",
        INSTRUCTIONS,
        "",
        ...sections.flatMap((s) => [s, ""]),
    ].join("\n");
}
