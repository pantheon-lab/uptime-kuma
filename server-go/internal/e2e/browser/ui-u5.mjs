// U5 against the built image: the monitor's page lists its outages, one shows on the chart, the
// chart takes a chosen range and the 30 day and 1 year periods, and the uptime figures choose them.
//
// Usage: node ui-u5.mjs <playwright-core dir> <chromium> <base URL> <screenshot dir>
//
// It needs a new database.
import { createRequire } from "node:module";
import path from "node:path";

const [pwDir, chromiumPath, baseURL, shots] = process.argv.slice(2);
const require = createRequire(path.join(pwDir, "index.js"));
const { chromium } = require(pwDir);
const password = "Kuma-E2E-U5-q8Rt3Bm6";

const browser = await chromium.launch({ executablePath: chromiumPath });
const context = await browser.newContext({ baseURL, viewport: { width: 1400, height: 1000 }, timezoneId: "UTC" });
const page = await context.newPage();
const errors = [];
page.on("pageerror", (e) => errors.push(e.message));
page.on("console", (m) => m.type() === "error" && /TypeError|ReferenceError/.test(m.text()) && errors.push(m.text()));
const results = [];

/**
 * Run a step and record whether it passed
 * @param {string} name Step
 * @param {Function} fn Body
 * @returns {Promise<void>}
 */
async function step(name, fn) {
    try {
        const detail = await fn();
        results.push(true);
        console.log(`PASS ${name}${detail ? ": " + detail : ""}`);
    } catch (e) {
        results.push(false);
        console.log(`FAIL ${name}: ${e.message}`);
        await page.screenshot({ path: path.join(shots, `fail-${name.replace(/\W+/g, "-")}.png`), fullPage: true });
    }
}

/**
 * Throw unless a condition holds
 * @param {boolean} ok Condition
 * @param {string} message What went wrong
 * @returns {void}
 * @throws {Error} When it doesn't
 */
function expect(ok, message) {
    if (!ok) {
        throw new Error(message);
    }
}

/**
 * Send a realtime event from the page, and return its answer
 * @param {...any} args Event and arguments
 * @returns {Promise<object>} Answer
 */
const emit = (...args) =>
    page.evaluate(
        (a) =>
            new Promise((resolve) =>
                document
                    .querySelector("#app")
                    .__vue_app__._container._vnode.component.proxy.getSocket()
                    .emit(...a, resolve)
            ),
        args
    );

/**
 * Set a manual monitor's status, and wait for the beat
 * @param {number} id Monitor
 * @param {number} status 1 for up, 0 for down
 * @returns {Promise<void>}
 */
async function setStatus(id, status) {
    const got = await emit("getMonitor", id);
    const res = await emit("editMonitor", { ...got.monitor, manual_status: status });
    expect(res.ok, JSON.stringify(res));
    await page.waitForFunction(
        ([id, status]) =>
            document.querySelector("#app").__vue_app__._container._vnode.component.proxy.lastHeartbeatList[id]
                ?.status === status,
        [id, status],
        { timeout: 20000 }
    );
}

const periodLabel = async () => (await page.locator(".btn-period-toggle").innerText()).replace(/\s+/g, " ").trim();
let id;

await step("set up a monitor with an outage that ended and one going on", async () => {
    const setup = await page.request.post("/api/setup", { data: { username: "admin", password } });
    expect((await setup.json()).ok === true, "setup failed");
    await page.goto("/dashboard");
    await page.getByPlaceholder("Username").fill("admin");
    await page.getByPlaceholder("Password").fill(password);
    await page.getByRole("button", { name: "Log in" }).click();
    await page.getByText("Quick Stats").waitFor();
    await page.goto("/add");
    await page.getByTestId("monitor-type-select").selectOption("manual");
    await page.getByTestId("friendly-name-input").fill("orders");
    await page.getByTestId("save-button").click();
    await page.waitForURL(/\/dashboard\/\d+/);
    id = Number(page.url().split("/").pop());
    await setStatus(id, 1);
    await setStatus(id, 0);
    await page.waitForTimeout(1500);
    await setStatus(id, 1);
    await page.waitForTimeout(1000);
    await setStatus(id, 0);
});

await step("the page lists the outages, newest first", async () => {
    await page.goto(`/dashboard/${id}`);
    const summary = page.getByTestId("outages-summary");
    await page.waitForFunction(
        () =>
            /2 outages in the last 30 days/.test(
                document.querySelector('[data-testid="outages-summary"]')?.textContent
            ),
        null,
        { timeout: 15000 }
    );
    const rows = page.getByTestId("outage");
    expect((await rows.count()) === 2, `${await rows.count()} rows`);
    const first = (await rows.nth(0).innerText()).replace(/\s+/g, " ");
    const second = (await rows.nth(1).innerText()).replace(/\s+/g, " ");
    expect(first.includes("Ongoing"), `first: ${first}`);
    expect(!second.includes("Ongoing") && second.includes("under a minute"), `second: ${second}`);
    return (await summary.innerText()).trim();
});

await step("a new status change updates the list", async () => {
    await setStatus(id, 1);
    await page.waitForFunction(
        () => !document.querySelector('[data-testid="outage"]')?.textContent.includes("Ongoing"),
        null,
        { timeout: 10000 }
    );
    expect((await page.getByTestId("outage").count()) === 2, "a row was added or lost");
});

await step("an outage shows on the chart", async () => {
    await page.getByTestId("outage-show").first().click();
    await page.waitForFunction(() => document.querySelector(".btn-period-toggle")?.textContent.includes("–"), null, {
        timeout: 10000,
    });
    const label = await periodLabel();
    expect(/^\d\d-\d\d \d\d:\d\d – \d\d-\d\d \d\d:\d\d$/.test(label), `label "${label}"`);
    await page.waitForFunction(() => !document.querySelector(".chart-wrapper.loading"), null, { timeout: 10000 });
    return label;
});

await step("a chosen range shows, and a backwards one is refused", async () => {
    await page.locator(".btn-period-toggle").click();
    await page.getByTestId("chart-period-custom").click();
    const form = page.getByTestId("chart-range-form");
    await form.waitFor();
    const inputs = form.locator('input[type="datetime-local"]');
    await inputs.nth(0).fill("2026-01-01T10:00");
    await inputs.nth(1).fill("2026-01-01T09:00");
    await form.getByRole("button", { name: "Show" }).click();
    await page.getByText("The range must start before it ends.").waitFor({ timeout: 5000 });
    await inputs.nth(1).fill("2026-01-01T16:30");
    await form.getByRole("button", { name: "Show" }).click();
    await page.waitForFunction(
        () => document.querySelector(".btn-period-toggle")?.textContent.includes("01-01 10:00 – 01-01 16:30"),
        null,
        { timeout: 10000 }
    );
    expect((await page.getByTestId("chart-range-form").count()) === 0, "the form stayed open");
});

await step("the uptime figures choose the chart's period, and the new periods load", async () => {
    // The refused range's pop-up, from the step before
    await page.evaluate(() => document.querySelectorAll(".Vue-Toastification__close-button").forEach((b) => b.click()));
    await page.waitForTimeout(800);
    const seen = [];
    for (const [type, label] of [
        ["720", "30d"],
        ["1y", "1y"],
        ["24", "24h"],
    ]) {
        await page.getByTestId(`uptime-${type}`).click();
        await page.waitForFunction(
            (l) => document.querySelector(".btn-period-toggle")?.textContent.trim() === l,
            label,
            {
                timeout: 10000,
            }
        );
        await page.waitForFunction(() => !document.querySelector(".chart-wrapper.loading"), null, { timeout: 10000 });
        seen.push(await periodLabel());
    }
    const toasts = await page.locator(".Vue-Toastification__toast--error").allInnerTexts();
    expect(toasts.length === 0, `error pop-ups: ${JSON.stringify(toasts)}`);
    return seen.join(", ");
});

await step("a shorter window of outages", async () => {
    await page.getByTestId("outages-days").selectOption("7");
    await page.waitForFunction(
        () => /in the last 7 days/.test(document.querySelector('[data-testid="outages-summary"]')?.textContent),
        null,
        { timeout: 10000 }
    );
    const r = await emit("getMonitorOutages", id, 400);
    expect(r.ok === false, "a year and more allowed");
});

await page.getByTestId("outage-show").first().click();
await page.waitForTimeout(1500);
await page.screenshot({ path: path.join(shots, "u5-monitor.png"), fullPage: true });
await browser.close();
const passed = results.filter(Boolean).length;
console.log(`${passed}/${results.length} passed; page errors: ${errors.length}`);
for (const e of errors.slice(0, 5)) {
    console.log("page error:", e);
}
process.exit(passed === results.length ? 0 : 1);
