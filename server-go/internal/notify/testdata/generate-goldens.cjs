// Records what the original Node.js notification providers send, as golden files for the Go
// providers. Run it from a checkout of commit d1788343, the last one with those providers:
//
//   git worktree add /tmp/nodesrc d1788343 && cd /tmp/nodesrc && npm ci
//   TZ=UTC node --import=tsx <this file> <output directory>
//
// Only the transport is replaced, so the provider code that runs is the real code.
const fs = require("fs");
const path = require("path");
const crypto = require("crypto");

const NOW = Date.UTC(2026, 0, 2, 3, 4, 5, 678);
const RealDate = Date;
globalThis.Date = class extends RealDate {
    constructor(...args) {
        if (args.length === 0) {
            super(NOW);
        } else {
            super(...args);
        }
    }
    static now() {
        return NOW;
    }
};
Math.random = () => 0.5;
crypto.randomBytes = (n) => Buffer.alloc(n, 0xab);
crypto.randomUUID = () => "11111111-2222-4333-8444-555555555555";

const axios = require(path.join(process.cwd(), "node_modules/axios"));
const dayjs = require(path.join(process.cwd(), "node_modules/dayjs"));
dayjs.extend(require(path.join(process.cwd(), "node_modules/dayjs/plugin/utc")));
dayjs.extend(require(path.join(process.cwd(), "node_modules/dayjs/plugin/timezone")));

// One response that satisfies every provider's success check, including token requests.
const RESPONSE = {
    ok: true, success: true, status: "success", result: { id: 1 }, code: 0, errcode: 0, errorCode: 0, ErrorCode: 0,
    Code: "OK", error: 0, errors: [], access_token: "stub-access-token", token: "stub-token", accessToken: "stub-access-token",
    tenant_access_token: "stub-tenant-token", expires_in: 3600, id: "stub-id", data: { id: "stub-id", token: "stub-token", ok: true },
    message: "ok", msg: "ok", ret: 0, event_id: "stub", sid: "stub",
};

let captured = [];
const SKIP_HEADERS = new Set(["accept", "user-agent", "content-length", "accept-encoding"]);
axios.defaults.adapter = async (config) => {
    const headers = {};
    const raw = config.headers && config.headers.toJSON ? config.headers.toJSON() : config.headers || {};
    for (const [name, value] of Object.entries(raw)) {
        if (!SKIP_HEADERS.has(name.toLowerCase()) && value !== undefined && value !== null && value !== false) {
            headers[name] = String(value);
        }
    }
    let url = config.url;
    if (config.baseURL && !/^https?:/.test(url)) {
        url = config.baseURL.replace(/\/$/, "") + "/" + url.replace(/^\//, "");
    }
    const request = {
        method: (config.method || "get").toUpperCase(),
        url: axios.getUri({ url, params: config.params, paramsSerializer: config.paramsSerializer }),
        headers,
    };
    if (config.auth) {
        request.basicAuth = config.auth;
    }
    if (config.data !== undefined && config.data !== null) {
        if (typeof config.data === "string") {
            request.body = config.data;
        } else if (Buffer.isBuffer(config.data)) {
            request.body = config.data.toString();
        } else if (typeof config.data.getBuffer === "function") {
            request.body = config.data.getBuffer().toString();
            request.multipart = true;
        } else {
            request.body = JSON.stringify(config.data);
        }
    }
    captured.push(request);
    return { status: 200, statusText: "OK", headers: {}, config, data: JSON.parse(JSON.stringify(RESPONSE)) };
};

// Providers read these from the database, which isn't available here.
const settings = require(path.join(process.cwd(), "server/settings")).Settings;
settings.get = async (key) => (key === "primaryBaseURL" ? "https://kuma.example" : null);

const NOT_HTTP = new Set(["smtp", "apprise", "nostr", "Webpush", "webpush"]);
const monitor = {
    id: 42, name: "Example Site", type: "http", url: "https://site.example/health", hostname: null, port: null,
    interval: 60, active: true, parent: null, pathName: "Team / Example Site", description: null, maxretries: 1, keyword: null, tags: [],
};
const heartbeat = (status, msg) => ({
    monitorID: 42, status, time: "2026-01-02 03:04:05.678", msg, ping: status === 1 ? 123 : null, important: true, duration: 60, retries: 0,
    timezone: "Asia/Hong_Kong", timezoneOffset: "+08:00", localDateTime: "2026-01-02 11:04:05",
    ...(status === 1 ? { lastDownTime: "2026-01-02 02:04:05.678" } : {}),
});
const SCENARIOS = {
    down: ["[Example Site] [🔴 Down] connect ECONNREFUSED", monitor, heartbeat(0, "connect ECONNREFUSED")],
    up: ["[Example Site] [✅ Up] 200 - OK", monitor, heartbeat(1, "200 - OK")],
    test: ["My Monitor Testing", null, null],
};

const OPTIONAL = /Use(Template|Custom|MessageTemplate)|UseOwnLine|UsePattern|Template(Format|ParseMode)?$|Custom(Title|Message|Body|Subject)|^custom|^htmlBody$|Enable|DisableUrl|^disableUrl$|Silent|SendSilently|Protect|^optionalParameters$|richmessage|channelnotify|Mentioned|^mentioning$|Prefix|Suppress|ThreadID|MaxRetries|Sound|sounds|device$|ttl$|CcEmail|BccEmail|CC$|BCC$|Icon|iconemo|iconurl|FromName$|Unsafe|IgnoreTls|MessagingService|ProfileId|PriorityDown|DontParseLinks|Subject$|Group$|GroupIds?$|Priority$|TimeSensitive|pushTitle|pushChannel|notiferyTitle|ntfyCall|smseagle|IncludeGroupName|button$/;
const URLISH = /url|uri|endpoint|host|server|domain|baseurl|webhook/i;
const value = (field) => (URLISH.test(field) ? `https://svc.example/${field}` : `v-${field}`);

/**
 * Every notification.<field> the provider reads gets a recognisable value.
 */
function fieldsOf(source) {
    const fields = new Set();
    for (const m of source.matchAll(/notification\.([A-Za-z0-9_]+)/g)) {
        fields.add(m[1]);
    }
    for (const m of source.matchAll(/notification\[["']([A-Za-z0-9_]+)["']\]/g)) {
        fields.add(m[1]);
    }
    return [...fields];
}

(async () => {
    const out = process.argv[2];
    fs.mkdirSync(out, { recursive: true });
    const dir = path.join(process.cwd(), "server/notification-providers");
    const summary = { recorded: [], noRequest: [], notHttp: [] };
    const overrides = fs.existsSync(path.join(__dirname, "overrides.json")) ? JSON.parse(fs.readFileSync(path.join(__dirname, "overrides.json"))) : {};

    for (const file of fs.readdirSync(dir).sort()) {
        if (file === "notification-provider.js") {
            continue;
        }
        const Provider = require(path.join(dir, file));
        const provider = new Provider();
        if (NOT_HTTP.has(provider.name)) {
            summary.notHttp.push(provider.name);
            continue;
        }
        const base = {};
        for (const field of fieldsOf(fs.readFileSync(path.join(dir, file), "utf8"))) {
            base[field] = value(field);
        }
        // Every field set takes the template and option paths, and without the optional ones the plain path.
        const toggles = Object.keys(base).filter((field) => OPTIONAL.test(field));
        const minimal = Object.fromEntries(toggles.map((field) => [field, null]));
        const variants = { default: {}, minimal, ...(overrides[provider.name] || {}) };
        const golden = { provider: provider.name, variants: {} };
        for (const [variant, extra] of Object.entries(variants)) {
            const config = { ...base, type: provider.name, ...extra };
            for (const field of Object.keys(config)) {
                if (config[field] === null) {
                    delete config[field];
                }
            }
            golden.variants[variant] = { config, scenarios: {} };
            for (const [name, [msg, m, h]] of Object.entries(SCENARIOS)) {
                captured = [];
                let error = null;
                try {
                    await provider.send(JSON.parse(JSON.stringify(config)), msg, m && JSON.parse(JSON.stringify(m)), h && JSON.parse(JSON.stringify(h)));
                } catch (e) {
                    error = e.message;
                }
                golden.variants[variant].scenarios[name] = { msg, requests: captured, error };
            }
        }
        const any = Object.values(golden.variants).some((v) => Object.values(v.scenarios).some((s) => s.requests.length > 0));
        (any ? summary.recorded : summary.noRequest).push(provider.name);
        fs.writeFileSync(path.join(out, provider.name.replace(/[^A-Za-z0-9_-]/g, "_") + ".json"), JSON.stringify(golden, null, 1) + "\n");
    }
    fs.writeFileSync(path.join(out, "_inputs.json"), JSON.stringify({ now: NOW, monitor, scenarios: { down: SCENARIOS.down[2], up: SCENARIOS.up[2] } }, null, 1) + "\n");
    console.log(JSON.stringify(summary));
    process.exit(0);
})();
