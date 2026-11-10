// Creates one monitor of each Phase 4f type through the web UI's form, then reads its first beat
// through the REST API, and checks that editing shows the saved settings.
//
// Usage: node monitor-types-4f.mjs <playwright-core dir> <chromium> <base URL> <admin token> <screenshot dir> <tailscale peer>
//
// It needs a new database, a Minecraft Java server on 127.0.0.1:55565, and tailscaled's socket mounted in the
// server's container, whose network must be the host's. The Globalping monitor uses the public API without a
// token, so it counts against the anonymous limit.
import { createRequire } from "node:module";
import path from "node:path";

const [pwDir, chromiumPath, baseURL, token, shots, tailscalePeer] = process.argv.slice(2);
const require = createRequire(path.join(pwDir, "index.js"));
const { chromium } = require(pwDir);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const browser = await chromium.launch({ executablePath: chromiumPath });
const context = await browser.newContext({ baseURL, viewport: { width: 1400, height: 1100 }, timezoneId: "UTC" });
const page = await context.newPage();

/**
 * Open the Edit Monitor form's Advanced section, which is folded until opened
 * @returns {Promise<void>}
 */
async function openAdvanced() {
    const toggle = page.getByTestId("advanced-toggle");
    if ((await toggle.count()) > 0 && (await toggle.getAttribute("aria-expanded")) === "false") {
        await toggle.click();
    }
}
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
        type: "gamedig",
        name: "Minecraft",
        fill: async () => {
            await page.locator("#game").selectOption("minecraft");
            await page.locator("#hostname").fill("127.0.0.1");
            await page.locator("#port").fill("55565");
        },
        want: (b) => b.status === 1 && b.msg === "Kuma test server",
        reopen: async () => (await page.locator("#game").inputValue()) === "minecraft",
    },
    {
        type: "tailscale-ping",
        name: "Tailscale peer",
        fill: async () => {
            await page.locator("#hostname").fill(tailscalePeer);
        },
        want: (b) => b.status === 1 && b.msg === "OK" && b.ping !== null,
        reopen: async () => (await page.locator("#hostname").inputValue()) === tailscalePeer,
    },
    {
        type: "globalping",
        name: "Globalping ping from Europe",
        fill: async () => {
            await page.getByTestId("monitor-subtype-select").selectOption("ping");
            await page.getByTestId("hostname-input").fill("example.com");
            await page.locator("#location").fill("Europe");
            await openAdvanced();
            await page.locator("#ping-count").fill("2");
        },
        want: (b) => b.status === 1 && b.msg.endsWith(" : OK"),
        reopen: async () => (await page.locator("#location").inputValue()) === "Europe",
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
