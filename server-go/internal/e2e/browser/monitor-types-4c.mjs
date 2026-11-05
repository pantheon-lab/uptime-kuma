// Creates one monitor of each Phase 4c type through the web UI's form, then reads its first beat
// through the REST API, and checks that editing shows the saved settings.
//
// Usage: node monitor-types-4c.mjs <playwright-core dir> <chromium> <base URL> <admin token> <screenshot dir>
//
// It needs a new database, and the SNMP agent and FreeRADIUS server that server-go/README.md describes,
// on this machine's ports, so the server must use the host's network. NTP and SMTP use public
// servers, pool.ntp.org and smtp.gmail.com.
import { createRequire } from "node:module";
import path from "node:path";

const [pwDir, chromiumPath, baseURL, token, shots] = process.argv.slice(2);
const require = createRequire(path.join(pwDir, "index.js"));
const { chromium } = require(pwDir);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const browser = await chromium.launch({ executablePath: chromiumPath });
const context = await browser.newContext({ baseURL, viewport: { width: 1400, height: 1100 }, timezoneId: "UTC" });
const page = await context.newPage();
const errors = [];
page.on("pageerror", (e) => errors.push(e.message));

await page.request.post("/api/setup", { data: { username: "admin", password: "Kuma-E2E-8f4Q2xR9p" } });
await page.goto("/dashboard");
await page.getByPlaceholder("Username").fill("admin");
await page.getByPlaceholder("Password").fill("Kuma-E2E-8f4Q2xR9p");
await page.getByRole("button", { name: "Log in" }).click();
await page.waitForSelector("text=Add New Monitor");

/**
 * Add a monitor through the form
 * @param {string} type Monitor type
 * @param {string} name Friendly name
 * @param {Function} fill Fills the type's fields
 * @param {Function} submit Saves the form, if not with the save button
 * @returns {Promise<string>} Monitor id
 */
async function add(type, name, fill, submit) {
    await page.goto("/add");
    await page.getByTestId("monitor-type-select").selectOption(type);
    await page.getByTestId("friendly-name-input").fill(name);
    await fill();
    await (submit ? submit() : page.getByTestId("save-button").click());
    await page.waitForURL(/\/dashboard\/\d+/, { timeout: 15000 });
    return page.url().split("/").pop();
}

/**
 * Wait for a monitor's first beat
 * @param {string} id Monitor id
 * @returns {Promise<object>} The beat
 */
async function firstBeat(id) {
    for (let i = 0; i < 60; i++) {
        const res = await page.request.get(`/api/v1/monitors/${id}/beats?limit=1`, {
            headers: { Authorization: `Bearer ${token}` },
        });
        const beats = await res.json();
        const list = Array.isArray(beats) ? beats : beats.beats || [];
        if (list.length > 0) {
            return list[0];
        }
        await sleep(500);
    }
    throw new Error(`no beat for monitor ${id}`);
}

const cases = [
    {
        type: "ntp",
        name: "NTP",
        fill: async () => {
            await page.locator("#hostname").fill("pool.ntp.org");
            await page.locator("#port").fill("123");
        },
        want: (b) => b.status === 1 && b.msg.startsWith("Stratum: "),
        reopen: async () => (await page.locator("#hostname").inputValue()) === "pool.ntp.org",
    },
    {
        type: "snmp",
        name: "SNMP uptime",
        fill: async () => {
            await page.locator("#hostname").fill("127.0.0.1");
            await page.locator("#port").fill("51161");
            await page.locator("#snmp_community_string input, input#snmp_community_string").first().fill("public");
            await page.locator("#snmp_oid").fill("1.3.6.1.2.1.1.3.0");
            await page.locator("#json_path_operator").selectOption(">");
            await page.locator("#expectedValue").fill("0");
        },
        want: (b) => b.status === 1 && b.msg.startsWith("JSON query passes (comparing "),
        reopen: async () => (await page.locator("#snmp_oid").inputValue()) === "1.3.6.1.2.1.1.3.0",
    },
    {
        type: "smtp",
        name: "SMTP with STARTTLS",
        fill: async () => {
            await page.locator("#hostname").fill("smtp.gmail.com");
            await page.locator("#port").fill("587");
            await page.locator("#smtp_security").selectOption("starttls");
        },
        want: (b) => b.status === 1 && b.msg === "SMTP connection verifies successfully",
        reopen: async () => (await page.locator("#smtp_security").inputValue()) === "starttls",
    },
    {
        type: "radius",
        name: "RADIUS",
        fill: async () => {
            await page.locator("#hostname").fill("127.0.0.1");
            await page.locator("#port").fill("51812");
            await page.locator("#radius_username").fill("kuma");
            await page.locator("#radius_password input, input#radius_password").first().fill("kumapass");
            await page.locator("#radius_secret input, input#radius_secret").first().fill("kumasecret");
            await page.locator("#radius_called_station_id").fill("AP-1");
            await page.locator("#radius_calling_station_id").fill("00-11-22-33-44-55");
        },
        want: (b) => b.status === 1 && b.msg === "Access-Accept",
        reopen: async () => (await page.locator("#radius_username").inputValue()) === "kuma",
    },
    {
        type: "steam",
        name: "Steam without a key",
        fill: async () => {
            await page.locator("#hostname").fill("127.0.0.1");
            await page.locator("#port").fill("27015");
        },
        // No key is set under Settings > General, so the check says so.
        want: (b) => b.status === 0 && b.msg === "Steam API Key not found",
        reopen: async () => (await page.locator("#port").inputValue()) === "27015",
    },
];

let failed = 0;
for (const c of cases) {
    try {
        if (c.before) {
            await c.before();
        }
        const id = await add(c.type, c.name, c.fill, c.submit);
        const beat = await firstBeat(id);
        if (!c.want(beat)) {
            throw new Error(`beat ${JSON.stringify(beat)}`);
        }
        await page.goto(`/edit/${id}`);
        await page.waitForSelector('[data-testid="friendly-name-input"]');
        await sleep(500);
        if (!(await c.reopen())) {
            throw new Error("the edit form doesn't show the saved settings");
        }
        console.log(`PASS ${c.type}: ${beat.msg}`);
    } catch (e) {
        failed++;
        console.log(`FAIL ${c.type}: ${e.message}`);
        await page.screenshot({ path: path.join(shots, `fail-${c.type}.png`), fullPage: true }).catch(() => {});
    }
}
await page.goto("/dashboard");
await sleep(1500);
await page.screenshot({ path: path.join(shots, "dashboard.png"), fullPage: true });
await browser.close();
console.log(`${cases.length - failed}/${cases.length} passed; page errors: ${errors.length}`);
for (const e of errors.slice(0, 5)) {
    console.log("page error:", e);
}
process.exit(failed ? 1 : 0);
