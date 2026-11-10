// U1 against the built image: the tab's title follows the page and counts the monitors down, the
// dashboard's counts filter the monitor list, and the Edit Monitor and Edit Maintenance pages ask
// before leaving with changes that aren't saved, and only then.
//
// Usage: node ui-u1.mjs <playwright-core dir> <chromium> <base URL> <screenshot dir>
//
// It needs a new database. The service must reach itself at http://127.0.0.1:3002.
import { createRequire } from "node:module";
import path from "node:path";

const [pwDir, chromiumPath, baseURL, shots] = process.argv.slice(2);
const require = createRequire(path.join(pwDir, "index.js"));
const { chromium } = require(pwDir);
const password = "Kuma-E2E-U1-p7Rk2Lw9";

const browser = await chromium.launch({ executablePath: chromiumPath });
const context = await browser.newContext({ baseURL, viewport: { width: 1400, height: 1000 }, timezoneId: "UTC" });
const page = await context.newPage();
const errors = [];
page.on("pageerror", (e) => errors.push(e.message));
const results = [];

// Dialogs are answered as each step sets, and counted
let dialogAnswer = "dismiss";
let dialogs = [];
page.on("dialog", async (dialog) => {
    dialogs.push(dialog.type());
    await (dialogAnswer === "accept" ? dialog.accept() : dialog.dismiss());
});

/**
 * Run a step and record whether it passed
 * @param {string} name Step
 * @param {Function} fn Body
 * @returns {Promise<void>}
 */
async function step(name, fn) {
    dialogs = [];
    dialogAnswer = "dismiss";
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
 * @param {Array} args Event and arguments
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
 * Wait for the tab's title
 * @param {string} title Expected title
 * @returns {Promise<string>} The title
 */
async function waitForTitle(title) {
    await page.waitForFunction((t) => document.title === t, title, { timeout: 15000 }).catch(() => {});
    const actual = await page.title();
    expect(actual === title, `title is "${actual}", not "${title}"`);
    return actual;
}

/**
 * The names in the monitor list
 * @returns {Promise<string[]>} Names
 */
async function listedNames() {
    await page.waitForTimeout(300);
    return (await page.getByTestId("monitor-list").locator(".item .text-truncate > .text-truncate").allInnerTexts())
        .map((s) => s.trim())
        .sort();
}

/**
 * Add an HTTP monitor with the form
 * @param {string} name Name
 * @param {string} url Address
 * @returns {Promise<string>} Its id
 */
async function addMonitor(name, url) {
    await page.goto("/add");
    await page.getByTestId("monitor-type-select").selectOption("http");
    await page.getByTestId("friendly-name-input").fill(name);
    await page.getByTestId("url-input").fill(url);
    await page.getByTestId("save-button").click();
    await page.waitForURL(/\/dashboard\/\d+/, { timeout: 15000 });
    return page.url().split("/").pop();
}

const ids = {};

await step("the login form's title", async () => {
    const setup = await page.request.post("/api/setup", { data: { username: "admin", password } });
    expect((await setup.json()).ok === true, "setup failed");
    await page.goto("/dashboard");
    await page.getByPlaceholder("Username").waitFor();
    return waitForTitle("Log in - Uptime Kuma");
});

await step("adding monitors leaves without asking", async () => {
    await page.getByPlaceholder("Username").fill("admin");
    await page.getByPlaceholder("Password").fill(password);
    await page.getByRole("button", { name: "Log in" }).click();
    await waitForTitle("Dashboard - Uptime Kuma");
    ids.up = await addMonitor("u1-up", "http://127.0.0.1:3002/");
    ids.down = await addMonitor("u1-down", "http://127.0.0.1:1/");
    ids.paused = await addMonitor("u1-paused", "http://127.0.0.1:3002/");
    const res = await emit("pauseMonitor", Number(ids.paused));
    expect(res.ok, JSON.stringify(res));
    expect(dialogs.length === 0, `asked ${dialogs.length} times`);
});

await step("the title counts the monitors down", async () => {
    await page.goto("/dashboard");
    return waitForTitle("(1) Dashboard - Uptime Kuma");
});

await step("the title names the monitor and the settings page", async () => {
    await page.goto(`/dashboard/${ids.up}`);
    const a = await waitForTitle("(1) u1-up - Uptime Kuma");
    await page.goto("/settings/reverse-proxy");
    const b = await waitForTitle("(1) Reverse Proxy - Settings - Uptime Kuma");
    return `${a} / ${b}`;
});

await step("the Down count filters the list, and again shows all", async () => {
    await page.goto("/dashboard");
    await page.getByTestId("quick-stat-up").waitFor();
    await page.waitForFunction(
        () => document.querySelector('[data-testid="quick-stat-down"] .num').textContent.trim() === "1",
        null,
        { timeout: 15000 }
    );
    expect((await listedNames()).length === 3, "the list doesn't start with all three");
    await page.getByTestId("quick-stat-down").click();
    const down = await listedNames();
    expect(JSON.stringify(down) === '["u1-down"]', `listed ${down}`);
    expect((await page.getByTestId("quick-stat-down").getAttribute("aria-pressed")) === "true", "not pressed");
    await page.getByTestId("quick-stat-down").click();
    const all = await listedNames();
    expect(all.length === 3, `listed ${all}`);
    expect((await page.getByTestId("quick-stat-down").getAttribute("aria-pressed")) === "false", "still pressed");
    return down.join();
});

await step("the Up and Paused counts filter the list", async () => {
    await page.getByTestId("quick-stat-up").click();
    const up = await listedNames();
    expect(JSON.stringify(up) === '["u1-up"]', `up listed ${up}`);
    await page.getByTestId("quick-stat-pause").click();
    const paused = await listedNames();
    expect(JSON.stringify(paused) === '["u1-paused"]', `paused listed ${paused}`);
    expect((await page.getByTestId("quick-stat-up").getAttribute("aria-pressed")) === "false", "up still pressed");
    await page.getByTestId("quick-stat-pause").click();
    return `${up} / ${paused}`;
});

await step("an edit page without changes leaves without asking", async () => {
    await page.goto(`/edit/${ids.up}`);
    await page.waitForFunction(() => document.querySelector('[data-testid="friendly-name-input"]')?.value === "u1-up");
    await page.waitForTimeout(500);
    await page.getByRole("link", { name: "Dashboard" }).click();
    await page.waitForURL(/\/dashboard$/);
    expect(dialogs.length === 0, `asked: ${dialogs}`);
});

await step("an edit page with a change asks, and stays when told to", async () => {
    await page.goto(`/edit/${ids.up}`);
    await page.waitForFunction(() => document.querySelector('[data-testid="friendly-name-input"]')?.value === "u1-up");
    await page.getByTestId("friendly-name-input").fill("u1-up-renamed");
    await page.getByRole("link", { name: "Dashboard" }).click();
    await page.waitForTimeout(500);
    expect(dialogs.length === 1 && dialogs[0] === "confirm", `dialogs: ${dialogs}`);
    expect(page.url().endsWith(`/edit/${ids.up}`), `left for ${page.url()}`);
    dialogAnswer = "accept";
    await page.getByRole("link", { name: "Dashboard" }).click();
    await page.waitForURL(/\/dashboard$/);
    expect(dialogs.length === 2, `dialogs: ${dialogs}`);
});

await step("saving an edit, then leaving, doesn't ask", async () => {
    await page.goto(`/edit/${ids.up}`);
    await page.waitForFunction(() => document.querySelector('[data-testid="friendly-name-input"]')?.value === "u1-up");
    await page.getByTestId("friendly-name-input").fill("u1-up-saved");
    await page.getByTestId("save-button").click();
    await page.waitForFunction(
        () => document.querySelector('[data-testid="friendly-name-input"]')?.value === "u1-up-saved"
    );
    await page.waitForTimeout(1000);
    await page.getByRole("link", { name: "Dashboard" }).click();
    await page.waitForURL(/\/dashboard$/);
    expect(dialogs.length === 0, `asked: ${dialogs}`);
});

await step("closing the tab with a change asks", async () => {
    await page.goto(`/edit/${ids.down}`);
    await page.waitForFunction(
        () => document.querySelector('[data-testid="friendly-name-input"]')?.value === "u1-down"
    );
    await page.getByTestId("friendly-name-input").fill("u1-down-changed");
    dialogAnswer = "dismiss";
    await page.close({ runBeforeUnload: true });
    await new Promise((r) => setTimeout(r, 500));
    expect(dialogs.includes("beforeunload"), `dialogs: ${dialogs}`);
    expect(!page.isClosed(), "the tab closed");
    // Close it for real, then carry on in a new tab
    page.removeAllListeners("dialog");
    page.on("dialog", (d) => d.accept());
    await page.close({ runBeforeUnload: true });
});

const page2 = await context.newPage();
page2.on("pageerror", (e) => errors.push(e.message));
page2.on("dialog", async (dialog) => {
    dialogs.push(dialog.type());
    await (dialogAnswer === "accept" ? dialog.accept() : dialog.dismiss());
});

await step("a new maintenance window asks only after a change", async () => {
    await page2.goto("/add-maintenance");
    await page2.locator("#name").waitFor();
    await page2.waitForTimeout(1500);
    await page2.getByRole("link", { name: "Dashboard" }).click();
    await page2.waitForURL(/\/dashboard$/);
    expect(dialogs.length === 0, `asked without a change: ${dialogs}`);
    await page2.goto("/add-maintenance");
    await page2.locator("#name").fill("u1 window");
    await page2.getByRole("link", { name: "Dashboard" }).click();
    await page2.waitForTimeout(500);
    expect(dialogs.length === 1, `dialogs: ${dialogs}`);
    expect(page2.url().endsWith("/add-maintenance"), `left for ${page2.url()}`);
});

await step("an existing maintenance window doesn't ask without a change", async () => {
    const res = await page2.evaluate(
        () =>
            new Promise((resolve) =>
                document
                    .querySelector("#app")
                    .__vue_app__._container._vnode.component.proxy.getSocket()
                    .emit(
                        "addMaintenance",
                        {
                            title: "u1 existing",
                            description: "",
                            strategy: "manual",
                            active: 1,
                            intervalDay: 1,
                            dateRange: [],
                            timeRange: [
                                { hours: 2, minutes: 0 },
                                { hours: 3, minutes: 0 },
                            ],
                            weekdays: [],
                            daysOfMonth: [],
                            timezoneOption: "SAME_AS_SERVER",
                        },
                        resolve
                    )
            )
    );
    expect(res.ok, JSON.stringify(res));
    dialogAnswer = "accept";
    await page2.goto(`/maintenance/edit/${res.maintenanceID}`);
    await page2.waitForFunction(() => document.querySelector("#name")?.value === "u1 existing");
    await page2.waitForTimeout(1500);
    dialogs = [];
    await page2.getByRole("link", { name: "Dashboard" }).click();
    await page2.waitForURL(/\/dashboard$/);
    expect(dialogs.length === 0, `asked without a change: ${dialogs}`);
    const title = await page2.title();
    expect(title === "(1) Dashboard - Uptime Kuma", `title ${title}`);
});

await step("on a phone, a count opens the list filtered", async () => {
    const phone = await browser.newPage({
        baseURL,
        viewport: { width: 390, height: 844 },
        storageState: await context.storageState(),
    });
    phone.on("pageerror", (e) => errors.push(e.message));
    await phone.goto("/dashboard");
    await phone.waitForFunction(
        () => document.querySelector('[data-testid="quick-stat-down"] .num')?.textContent.trim() === "1",
        null,
        { timeout: 15000 }
    );
    await phone.getByTestId("quick-stat-down").click();
    await phone.waitForURL(/\/list$/);
    await phone.waitForTimeout(500);
    const names = (
        await phone.getByTestId("monitor-list").locator(".item .text-truncate > .text-truncate").allInnerTexts()
    ).map((n) => n.trim());
    await phone.close();
    expect(JSON.stringify(names) === '["u1-down"]', `listed ${names}`);
    return names.join();
});

await step("help links point to this fork", async () => {
    await page2.goto("/settings/reverse-proxy");
    const link = page2.getByRole("link", { name: "How to set up a reverse proxy for this service" });
    const href = await link.getAttribute("href");
    expect(href.endsWith("/pantheon-lab/uptime-kuma/tree/main/server-go#behind-a-reverse-proxy"), href);
    const html = await page2.content();
    expect(!html.includes("louislam/uptime-kuma/wiki"), "an upstream wiki link is left");
    return href;
});

await browser.close();
const passed = results.filter(Boolean).length;
console.log(`${passed}/${results.length} passed; page errors: ${errors.length}`);
for (const e of errors.slice(0, 5)) {
    console.log("page error:", e);
}
process.exit(passed === results.length ? 0 : 1);
