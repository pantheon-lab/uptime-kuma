// Adds a proxy in the settings, then an HTTP monitor that uses it with the Phase 4e options, and
// checks that the check went through the proxy, that editing shows the options, that the monitor's
// page shows its description and its domain's expiry, and that the form explains a target whose
// domain can't be tracked.
//
// Usage: node monitor-options-4e.mjs <playwright-core dir> <chromium> <base URL> <admin token> <screenshot dir> <proxy port>
//
// It needs a new database, and on this machine a forward proxy with the login u:p that answers
// GET /count with how many requests it carried. The server must use the host's network. The
// monitor checks https://example.com, and its domain's expiry comes from Verisign's RDAP server.
import { createRequire } from "node:module";
import path from "node:path";

const [pwDir, chromiumPath, baseURL, token, shots, proxyPort] = process.argv.slice(2);
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
 * Wait for a toast with the text
 * @param {string} text Text the toast starts with
 * @returns {Promise<void>}
 */
async function toast(text) {
    try {
        await page.getByText(text).first().waitFor({ timeout: 15000 });
    } catch (e) {
        await page.screenshot({ path: path.join(shots, "fail-toast.png"), fullPage: true });
        throw new Error(
            `no toast "${text}"; the page says: ${(await page.locator("body").innerText()).slice(0, 2000)}`
        );
    }
}

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

let failed = 0;
/**
 * Run one step, and report it
 * @param {string} name Step name
 * @param {Function} fn The step
 * @returns {Promise<void>}
 */
async function step(name, fn) {
    try {
        console.log(`PASS ${name}: ${(await fn()) || "ok"}`);
    } catch (e) {
        failed++;
        console.log(`FAIL ${name}: ${e.message}`);
        await page
            .screenshot({ path: path.join(shots, `fail-${name.replace(/\W+/g, "-")}.png`), fullPage: true })
            .catch(() => {});
    }
}

const proxyCount = async () => Number(await (await page.request.get(`http://127.0.0.1:${proxyPort}/count`)).text());

await step("proxy", async () => {
    await page.goto("/settings/proxies");
    await page.getByRole("button", { name: "Set Up Proxy" }).click();
    await page.locator("#proxy-protocol").selectOption("http");
    await page.locator("#proxy-host").fill("127.0.0.1");
    await page.locator(".modal.show").getByPlaceholder("Port").fill(proxyPort);
    await page.locator("#mark-auth").check();
    await page.locator("#proxy-username").fill("u");
    await page.locator("#proxy-password input, input#proxy-password").first().fill("p");
    await page.locator(".modal.show button[type=submit]").click();
    await toast("Saved");
    return "saved";
});

let id;
await step("keyword monitor through the proxy", async () => {
    const before = await proxyCount();
    id = await add("keyword", "Example through the proxy", async () => {
        await page.getByTestId("url-input").fill("https://example.com");
        await page.locator("#keyword").fill("Example Domain");
        await page.locator("#proxy-1").check();
        await openAdvanced();
        await page.locator("#cache-bust").check();
        await page.locator("#ipFamily").first().selectOption("ipv4");
        await page.locator("#description").fill("Checked through the proxy");
        await page.locator("#domain-expiry-notification").check();
    });
    const beat = await firstBeat(id);
    if (beat.status !== 1 || !beat.msg.endsWith("keyword is found")) {
        throw new Error(`beat ${JSON.stringify(beat)}`);
    }
    const after = await proxyCount();
    if (after <= before) {
        throw new Error("the check didn't go through the proxy");
    }
    return `${beat.msg}, ${after - before} request(s) through the proxy`;
});

await step("edit form shows the options", async () => {
    await page.goto(`/edit/${id}`);
    await page.waitForSelector('[data-testid="friendly-name-input"]');
    await sleep(500);
    const checks = {
        proxy: await page.locator("#proxy-1").isChecked(),
        cacheBust: await page.locator("#cache-bust").isChecked(),
        ipFamily: (await page.locator("#ipFamily").first().inputValue()) === "ipv4",
        description: (await page.locator("#description").inputValue()) === "Checked through the proxy",
        domainExpiry: await page.locator("#domain-expiry-notification").isChecked(),
    };
    const wrong = Object.keys(checks).filter((k) => !checks[k]);
    if (wrong.length > 0) {
        throw new Error(`not shown: ${wrong.join(", ")}`);
    }
});

await step("monitor page shows the description and domain expiry", async () => {
    await page.goto(`/dashboard/${id}`);
    await page.getByText("Checked through the proxy").first().waitFor({ timeout: 15000 });
    // The lookup runs after the first check, so a reload may be needed to see it.
    for (let i = 0; i < 20; i++) {
        if ((await page.getByText("Domain Exp.").count()) > 0) {
            const box = page.locator("div", { has: page.getByText("Domain Exp.") }).last();
            return (await box.innerText()).replace(/\s+/g, " ");
        }
        await sleep(1500);
        await page.reload();
    }
    throw new Error("no domain expiry");
});

await step("form explains an untracked domain", async () => {
    await page.goto("/add");
    await page.getByTestId("monitor-type-select").selectOption("ping");
    await page.locator("#hostname").fill("10.0.0.1");
    await openAdvanced();
    await page.locator("#domain-expiry-notification").check();
    await page.getByText("is not a candidate for domain expiry monitoring").first().waitFor({ timeout: 10000 });
});

await browser.close();
console.log(`${5 - failed}/5 passed; page errors: ${errors.length}`);
for (const e of errors.slice(0, 5)) {
    console.log("page error:", e);
}
process.exit(failed ? 1 : 0);
