import { describe, test } from "node:test";
import assert from "node:assert";
import { buildIncidentPrompt, buildMonitorPrompt, redactText, redactURL, reproduceCommands } from "../../src/ai-prompt";

const now = new Date("2026-11-08T12:00:00Z");

// A monitor as an admin's page has it, secrets and all
const http = {
    id: 7,
    name: "api",
    pathName: "Backend / api",
    type: "http",
    url: "https://bob:hunter2@api.example.com/health?token=abc123&region=eu",
    method: "post",
    accepted_statuscodes: ["200-299"],
    interval: 60,
    retryInterval: 30,
    maxretries: 2,
    timeout: 48,
    headers: '{"Authorization": "Bearer sk-live-SECRET-HEADER", "X-Team": "core"}',
    body: '{"password": "BODY-SECRET"}',
    basic_auth_user: "bob",
    basic_auth_pass: "BASIC-SECRET",
    oauth_client_secret: "OAUTH-SECRET",
    pushToken: "PUSH-SECRET",
    tlsKey: "TLS-KEY-SECRET",
    proxyId: 3,
    active: true,
    tags: [{ name: "team", value: "core" }],
};

const beats = [
    { status: 1, time: "2026-11-08 09:00:00.000", msg: "200 - OK", ping: 120 },
    { status: 1, time: "2026-11-08 09:01:00.000", msg: "200 - OK", ping: 180 },
    {
        status: 0,
        time: "2026-11-08 09:02:00.000",
        msg: 'Get "https://bob:hunter2@api.example.com/health?token=abc123": connection refused',
        ping: null,
        traceId: "4bf92f3577b34da6a3ce929d0e0e4736",
    },
];
const events = [
    { status: 1, time: "2026-11-07 08:00:00.000", msg: "200 - OK" },
    { status: 0, time: "2026-11-08 09:02:00.000", msg: "connection refused" },
];

describe("buildMonitorPrompt()", () => {
    const prompt = buildMonitorPrompt(
        {
            monitor: http,
            beats,
            events,
            uptime24: 0.9512,
            uptime30d: 0.99,
            certificate: { validTo: "2027-01-01", daysRemaining: 54, issuer: { O: "Let's Encrypt" } },
        },
        { now, baseURL: "https://kuma.example.com/" }
    );

    test("leaves out every secret", () => {
        for (const secret of [
            "hunter2",
            "abc123",
            "sk-live",
            "SECRET-HEADER",
            "BODY-SECRET",
            "BASIC-SECRET",
            "OAUTH-SECRET",
            "PUSH-SECRET",
            "TLS-KEY-SECRET",
        ]) {
            assert.ok(!prompt.includes(secret), `${secret} leaked`);
        }
        assert.ok(prompt.includes("region=eu"), "a harmless query value was removed");
        assert.ok(prompt.includes("Authorization, X-Team (values withheld)"));
    });

    test("says what failed, since when, and the trace", () => {
        assert.ok(prompt.startsWith("# Investigate a failing monitor: Backend / api"));
        assert.ok(prompt.includes("- Status: DOWN, DOWN since 2026-11-08 09:02:00 UTC (2 h 58 min)"));
        assert.ok(prompt.includes("`4bf92f3577b34da6a3ce929d0e0e4736`"));
        assert.ok(prompt.includes("150 ms on average, 180 ms at most"));
        assert.ok(prompt.includes("95.12% over 24 hours, 99.00% over 30 days"));
        assert.ok(prompt.includes("issued by Let's Encrypt"));
        assert.ok(prompt.includes("https://kuma.example.com/dashboard/7"));
        assert.ok(prompt.includes("- Method: POST"));
        assert.ok(
            prompt.includes("- Checked every: 60s, and after a failure every 30s, up to 2 more times before DOWN")
        );
    });

    test("lists checks newest first, and gives commands", () => {
        const rows = prompt.split("\n").filter((l) => l.startsWith("| 2026"));
        assert.strictEqual(rows.length, 3);
        assert.ok(rows[0].includes("| DOWN |"));
        assert.ok(prompt.includes("```sh\ncurl -sS"));
        assert.ok(prompt.includes("-X POST"));
        assert.ok(prompt.includes("openssl s_client -connect 'api.example.com:443'"));
        assert.ok(prompt.includes("--proxy"));
    });
});

describe("reproduceCommands()", () => {
    test("by type", () => {
        assert.deepStrictEqual(
            reproduceCommands({ id: 1, name: "p", type: "port", hostname: "db.internal", port: 5432, timeout: 10 }),
            ["nc -vz -w 10 'db.internal' 5432", "dig +short 'db.internal'"]
        );
        assert.ok(
            reproduceCommands({
                id: 1,
                name: "d",
                type: "dns",
                hostname: "example.com",
                dns_resolve_type: "MX",
                dns_resolve_server: "1.1.1.1",
            })[0] === "dig @1.1.1.1 'example.com' MX"
        );
        assert.ok(
            reproduceCommands({ id: 1, name: "c", type: "docker", docker_container: "web" })[0].startsWith(
                "docker ps -a"
            )
        );
        assert.deepStrictEqual(reproduceCommands({ id: 1, name: "push", type: "push" }), []);
    });

    test("a database's host and port, not its connection string", () => {
        const commands = reproduceCommands({
            id: 1,
            name: "pg",
            type: "postgres",
            databaseConnectionString: "postgres://kuma:DB-SECRET@pg.internal/kuma",
        }).join("\n");
        assert.ok(commands.includes("nc -vz -w 30 'pg.internal' 5432"));
        assert.ok(!commands.includes("DB-SECRET"));
        const sql = reproduceCommands({
            id: 1,
            name: "mssql",
            type: "sqlserver",
            databaseConnectionString: "Server=sql.internal,1444;Database=x;User Id=sa;Password=SQL-SECRET",
        }).join("\n");
        assert.ok(sql.includes("'sql.internal' 1444"));
        assert.ok(!sql.includes("SQL-SECRET"));
    });

    test("commands leave credentials out of the address, and say how to add them", () => {
        const commands = reproduceCommands({ ...http, authMethod: "basic" }).join("\n");
        assert.ok(commands.includes("'https://api.example.com/health?token=REDACTED&region=eu'"));
        assert.ok(!commands.includes("@api.example.com"));
        assert.ok(commands.includes("# Put the real values where the address says REDACTED."));
        assert.ok(commands.includes("# The monitor logs in (basic)"));
        const plain = reproduceCommands({ id: 1, name: "u", type: "http", url: "https://u:p@x.example/" }).join("\n");
        assert.ok(plain.includes("add them with curl's -u"));
    });

    test("no name lookup for an address", () => {
        const commands = reproduceCommands({ id: 1, name: "ip", type: "port", hostname: "10.0.0.5", port: 22 });
        assert.deepStrictEqual(commands, ["nc -vz -w 30 '10.0.0.5' 22"]);
    });

    test("quotes for the shell", () => {
        const [cmd] = reproduceCommands({ id: 1, name: "q", type: "http", url: "https://x.example/it's" });
        assert.ok(cmd.endsWith("'https://x.example/it'\\''s'"));
    });
});

describe("redaction", () => {
    test("addresses", () => {
        assert.strictEqual(
            redactURL("https://u:p@h.example/x?api_key=1&page=2"),
            "https://REDACTED@h.example/x?api_key=REDACTED&page=2"
        );
        assert.strictEqual(redactURL("not a url ?token=abc"), "not a url ?token=REDACTED");
    });

    test("messages", () => {
        assert.strictEqual(
            redactText("dial mongodb://admin:pw@m:27017 failed"),
            "dial mongodb://REDACTED@m:27017 failed"
        );
        assert.strictEqual(redactText("Authorization: Bearer abcdefghijkl"), "Authorization: Bearer REDACTED");
    });
});

describe("buildIncidentPrompt()", () => {
    test("several monitors, asking about a shared cause first", () => {
        const dns = { id: 2, name: "dns", type: "dns", hostname: "example.com" };
        const prompt = buildIncidentPrompt(
            [
                { monitor: http, beats, events },
                { monitor: dns, beats: [{ status: 0, time: "2026-11-08 11:00:00.000", msg: "SERVFAIL" }], events: [] },
            ],
            { now }
        );
        assert.ok(prompt.startsWith("# Investigate 2 failing monitors"));
        assert.ok(prompt.includes("whether they share a cause"));
        assert.ok(prompt.includes("## Backend / api\n\n### The monitor"));
        assert.ok(prompt.includes("## dns\n\n### The monitor"));
        assert.ok(!prompt.includes("hunter2"));
    });

    test("one monitor is the single prompt", () => {
        const one = buildIncidentPrompt([{ monitor: http, beats, events }], { now });
        assert.ok(one.startsWith("# Investigate a failing monitor"));
    });

    test("an all-zero trace id isn't given", () => {
        const prompt = buildMonitorPrompt(
            {
                monitor: { id: 1, name: "x", type: "port", hostname: "h", port: 1 },
                beats: [{ status: 0, time: "2026-11-08 11:00:00.000", msg: "refused", traceId: "0".repeat(32) }],
                events: [],
            },
            { now }
        );
        assert.ok(!prompt.includes("Trace ID"));
    });

    test("a group lists its failing monitors", () => {
        const prompt = buildMonitorPrompt(
            {
                monitor: { id: 10, name: "Backend", type: "group" },
                beats: [],
                events: [],
                failingChildren: [{ name: "api", status: 0, msg: "timeout" }],
            },
            { now }
        );
        assert.ok(prompt.includes("- api: DOWN (timeout)"));
        assert.ok(prompt.includes("- Status: no checks yet"));
        const once = buildMonitorPrompt(
            {
                monitor: { id: 1, name: "o", type: "ping", hostname: "h", interval: 20, maxretries: 0 },
                beats: [],
                events: [],
            },
            { now }
        );
        assert.ok(once.includes("- Checked every: 20s, DOWN at the first failure"));
    });
});
