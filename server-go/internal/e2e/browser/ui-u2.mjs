// U2 against the built image: the dashboard's events filter by status, monitor and time, the
// filter is remembered, Clear All Events sits in a menu and clears every monitor's events at once,
// monitors that go down together share one pop-up, and a muted monitor shows none.
//
// Usage: node ui-u2.mjs <playwright-core dir> <chromium> <base URL> <screenshot dir>
//
// It needs a new database.
import { createRequire } from "node:module";
import path from "node:path";

const [pwDir, chromiumPath, baseURL, shots] = process.argv.slice(2);
const require = createRequire(path.join(pwDir, "index.js"));
const { chromium } = require(pwDir);
const password = "Kuma-E2E-U2-x3Vb8Qn5";

const browser = await chromium.launch({ executablePath: chromiumPath });
const context = await browser.newContext({ baseURL, viewport: { width: 1400, height: 1000 }, timezoneId: "UTC" });
const page = await context.newPage();
const errors = [];
page.on("pageerror", (e) => errors.push(e.message));
page.on("dialog", (d) => d.accept());
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
 * Set manual monitors' status at once, from the page
 * @param {string[]} ids Monitor ids
 * @param {number} status 1 for up, 0 for down
 * @returns {Promise<object[]>} The answers
 */
const setManual = (ids, status) =>
    page.evaluate(
        async ([ids, status]) => {
            const socket = document.querySelector("#app").__vue_app__._container._vnode.component.proxy.getSocket();
            const call = (...args) => new Promise((resolve) => socket.emit(...args, resolve));
            const monitors = await Promise.all(ids.map((id) => call("getMonitor", Number(id))));
            return Promise.all(monitors.map((res) => call("editMonitor", { ...res.monitor, manual_status: status })));
        },
        [ids, status]
    );

/**
 * The texts of the pop-ups shown
 * @returns {Promise<string[]>} Texts
 */
const toasts = () => page.locator(".Vue-Toastification__toast-body").allInnerTexts();

/**
 * Close every pop-up
 * @returns {Promise<void>}
 */
async function dismissToasts() {
    await page.evaluate(() => {
        for (const button of document.querySelectorAll(".Vue-Toastification__close-button")) {
            button.click();
        }
    });
    await page.waitForTimeout(800);
}

/**
 * The monitor names in the events table
 * @returns {Promise<string[]>} Names, newest first
 */
async function eventNames() {
    await page.waitForTimeout(800);
    return (await page.locator("table tbody tr td.name-column").allInnerTexts()).map((s) => s.trim());
}

const ids = {};

await step("set up three manual monitors, up", async () => {
    const setup = await page.request.post("/api/setup", { data: { username: "admin", password } });
    expect((await setup.json()).ok === true, "setup failed");
    await page.goto("/dashboard");
    await page.getByPlaceholder("Username").fill("admin");
    await page.getByPlaceholder("Password").fill(password);
    await page.getByRole("button", { name: "Log in" }).click();
    await page.getByText("Quick Stats").waitFor();
    for (const name of ["alpha", "bravo", "charlie"]) {
        await page.goto("/add");
        await page.getByTestId("monitor-type-select").selectOption("manual");
        await page.getByTestId("friendly-name-input").fill(name);
        await page.getByTestId("save-button").click();
        await page.waitForURL(/\/dashboard\/\d+/, { timeout: 15000 });
        ids[name] = page.url().split("/").pop();
    }
    const res = await setManual(Object.values(ids), 1);
    expect(
        res.every((r) => r.ok),
        JSON.stringify(res)
    );
    await page.goto("/dashboard");
    await page.waitForFunction(
        () => document.querySelector('[data-testid="quick-stat-up"] .num')?.textContent.trim() === "3",
        null,
        { timeout: 20000 }
    );
    await dismissToasts();
});

await step("three monitors going down together share one pop-up", async () => {
    await dismissToasts();
    const res = await setManual(Object.values(ids), 0);
    expect(
        res.every((r) => r.ok),
        JSON.stringify(res)
    );
    await page.waitForFunction(
        () => document.querySelector('[data-testid="quick-stat-down"] .num')?.textContent.trim() === "3",
        null,
        { timeout: 20000 }
    );
    await page.waitForTimeout(2000);
    const texts = await toasts();
    const grouped = texts.filter((t) => t.startsWith("3 monitors are down"));
    const single = texts.filter((t) => t.includes("[DOWN]"));
    expect(grouped.length === 1 && single.length === 0, `pop-ups: ${JSON.stringify(texts)}`);
    return grouped[0];
});

await step("a muted monitor shows no pop-up", async () => {
    await page.goto(`/dashboard/${ids.alpha}`);
    await page.getByTestId("mute-toasts").click();
    expect((await page.getByTestId("mute-toasts").getAttribute("aria-pressed")) === "true", "not pressed");
    await page.goto("/dashboard");
    await dismissToasts();
    await setManual(Object.values(ids), 1);
    await page.waitForFunction(
        () => document.querySelector('[data-testid="quick-stat-up"] .num')?.textContent.trim() === "3",
        null,
        { timeout: 20000 }
    );
    await page.waitForTimeout(2000);
    const texts = await toasts();
    const grouped = texts.filter((t) => t.startsWith("2 monitors are up"));
    expect(grouped.length === 1 && !grouped[0].includes("alpha"), `pop-ups: ${JSON.stringify(texts)}`);
    expect(!texts.some((t) => t.includes("alpha")), `alpha shown: ${JSON.stringify(texts)}`);
    return grouped[0];
});

await step("the events filter by status", async () => {
    await dismissToasts();
    const all = await eventNames();
    await page.getByTestId("event-filter-status").selectOption({ label: "Down" });
    const down = await eventNames();
    const statuses = await page.locator("table tbody tr td .badge").allInnerTexts();
    expect(down.length === 3 && statuses.every((s) => s.trim() === "Down"), `down: ${down} ${statuses}`);
    expect(all.length > down.length, `all ${all.length}, down ${down.length}`);
    return `${all.length} events, ${down.length} down`;
});

await step("the events filter by monitor, and remember the filter", async () => {
    await page.getByTestId("event-filter-monitor").selectOption({ label: "bravo" });
    const names = await eventNames();
    expect(JSON.stringify(names) === '["bravo"]', `names: ${names}`);
    await page.reload();
    await page.getByTestId("event-filter-monitor").waitFor();
    const kept = await eventNames();
    expect(JSON.stringify(kept) === '["bravo"]', `after reload: ${kept}`);
    expect((await page.getByTestId("event-filter-status").inputValue()) === "0", "status forgotten");
});

await step("the events filter by time, and clear", async () => {
    await page.getByTestId("event-filter-range").selectOption("1h");
    expect((await eventNames()).length === 1, "an event from now isn't in the last hour");
    await page.getByTestId("event-filter-clear").click();
    const names = await eventNames();
    expect(names.length >= 6, `cleared shows ${names.length}`);
    expect((await page.getByTestId("event-filter-clear").count()) === 0, "clear still shown");
});

await step("a new event outside the filter isn't added", async () => {
    await page.getByTestId("event-filter-status").selectOption({ label: "Up" });
    const before = await eventNames();
    await setManual([ids.charlie], 0);
    await page.waitForTimeout(3000);
    const after = await eventNames();
    expect(after.length === before.length, `before ${before.length}, after ${after.length}`);
    await setManual([ids.charlie], 1);
    await page.waitForTimeout(3000);
    const again = await eventNames();
    expect(again.length === before.length + 1 && again[0] === "charlie", `then: ${again}`);
});

await step("Clear All Events is in the menu, and clears every monitor's", async () => {
    await page.getByTestId("event-filter-status").selectOption("");
    await dismissToasts();
    expect(await page.getByTestId("clear-all-events").isHidden(), "shown outside the menu");
    await page.getByTestId("events-menu").click();
    await page.getByTestId("clear-all-events").click();
    await page.getByRole("button", { name: "Yes" }).click();
    await page.getByTestId("events-empty").waitFor({ timeout: 10000 });
    await page.waitForTimeout(500);
    const texts = await toasts();
    expect(
        texts.some((t) => t.includes("Events cleared successfully")),
        `pop-ups: ${JSON.stringify(texts)}`
    );
    const count = await page.evaluate(
        () =>
            new Promise((resolve) =>
                document
                    .querySelector("#app")
                    .__vue_app__._container._vnode.component.proxy.getSocket()
                    .emit("monitorImportantHeartbeatListCount", null, resolve)
            )
    );
    expect(count.ok && count.count === 0, JSON.stringify(count));
});

await step("the server refuses a bad filter", async () => {
    const res = await page.evaluate(
        () =>
            new Promise((resolve) =>
                document
                    .querySelector("#app")
                    .__vue_app__._container._vnode.component.proxy.getSocket()
                    .emit("monitorImportantHeartbeatListCount", null, { statuses: [9] }, resolve)
            )
    );
    expect(res.ok === false, JSON.stringify(res));
    return res.msg;
});

await page.goto("/dashboard");
await page.screenshot({ path: path.join(shots, "u2-dashboard.png") });
await browser.close();
const passed = results.filter(Boolean).length;
console.log(`${passed}/${results.length} passed; page errors: ${errors.length}`);
for (const e of errors.slice(0, 5)) {
    console.log("page error:", e);
}
process.exit(passed === results.length ? 0 : 1);
