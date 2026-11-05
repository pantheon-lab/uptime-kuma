// Creates one monitor of each Phase 4a type through the web UI's form, then reads its first beat
// through the REST API, and checks that editing shows the saved settings.
//
// Usage: node monitor-types.mjs <playwright-core dir> <chromium> <base URL> <admin token> <screenshot dir>
//
// It needs a new database, and on the server's network: PostgreSQL at p4db (postgres:pw, database
// kuma), MariaDB at p4maria (root:pw) and Redis at p4redis. The WebSocket and DNS checks need the
// internet.
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

/**
 * Add a condition to the form
 * @param {string} operator Operator id
 * @param {string} value Value
 * @returns {Promise<void>}
 */
async function addCondition(operator, value) {
    await page.getByTestId("add-condition-button").click();
    await page.getByTestId("condition-operator").last().selectOption(operator);
    await page.getByTestId("condition-value").last().fill(value);
}

const cases = [
    {
        type: "json-query",
        name: "JSON health",
        fill: async () => {
            await page.getByTestId("url-input").fill("http://127.0.0.1:3002/healthz");
            await page.locator("#jsonPath").fill("status");
            await page.locator("#json_path_operator").selectOption("==");
            await page.locator("#expectedValue").fill("ok");
        },
        want: (b) => b.status === 1 && b.msg === "JSON query passes (comparing ok == ok)",
        reopen: async () => (await page.locator("#jsonPath").inputValue()) === "status",
    },
    {
        type: "dns",
        name: "DNS with a condition",
        fill: async () => {
            await page.locator("#hostname").fill("example.com");
            await addCondition("contains", ".");
        },
        want: (b) => b.status === 1 && b.msg.startsWith("Records: "),
        reopen: async () => (await page.getByTestId("condition-value").first().inputValue()) === ".",
    },
    {
        type: "postgres",
        name: "Postgres",
        fill: async () => {
            await page.locator("#connectionString").fill("postgres://postgres:pw@p4db:5432/kuma");
            await page.locator("#sqlQuery").fill("SELECT count(*) FROM monitor");
        },
        want: (b) => b.status === 1,
        reopen: async () => (await page.locator("#sqlQuery").inputValue()) === "SELECT count(*) FROM monitor",
    },
    {
        type: "mysql",
        name: "MariaDB with a condition",
        fill: async () => {
            await page.locator("#connectionString").fill("mysql://root@p4maria:3306/mysql");
            await page.locator("#mysql-password input, input#mysql-password").first().fill("pw");
            await page.locator("#sqlQuery").fill("SELECT 7");
            await addCondition("equals", "7");
        },
        want: (b) => b.status === 1 && b.msg === "Query did meet specified conditions",
        reopen: async () => (await page.getByTestId("condition-value").first().inputValue()) === "7",
    },
    {
        type: "redis",
        name: "Redis",
        fill: async () => {
            await page.locator("#connectionString").fill("redis://p4redis:6379");
        },
        want: (b) => b.status === 1 && b.msg === "PONG",
        reopen: async () => (await page.locator("#connectionString").inputValue()) === "redis://p4redis:6379",
    },
    {
        type: "websocket-upgrade",
        name: "WebSocket echo",
        fill: async () => {
            await page.getByTestId("url-input").fill("wss://ws.postman-echo.com/raw");
        },
        want: (b) => b.status === 1 && b.msg === "1000 - OK",
        reopen: async () => (await page.getByTestId("url-input").inputValue()) === "wss://ws.postman-echo.com/raw",
    },
    {
        type: "manual",
        name: "Manual",
        // The form's Up and Down buttons are inside the form, so a click also saves it.
        fill: async () => {},
        submit: async () => {
            await page.getByRole("button", { name: "Down" }).first().click();
        },
        want: (b) => b.status === 0 && b.msg === "Down",
        reopen: async () => true,
    },
];

let failed = 0;
for (const c of cases) {
    try {
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
