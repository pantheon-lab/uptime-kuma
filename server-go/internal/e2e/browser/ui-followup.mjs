// What U1 to U3 and the AI prompt left untested, against the built image: a failed save keeping
// the form, muting pop-ups by tag, a group in the events filter, a viewer's palette, menu and
// shortcuts, ⌘K and the shortcuts' list on a Mac, "/" on a phone, and copying the prompt over
// plain HTTP, without the clipboard API.
//
// Usage: node ui-followup.mjs <playwright-core dir> <chromium> <base URL> <plain HTTP URL> <screenshot dir> <psql command>
//
// The plain HTTP URL must reach the same service at an address that isn't localhost, so that the
// browser doesn't count it as secure. The psql command runs SQL in the service's new database.
import { execSync } from "node:child_process";
import { createRequire } from "node:module";
import path from "node:path";

const [pwDir, chromiumPath, baseURL, plainURL, shots, psqlCommand] = process.argv.slice(2);
const require = createRequire(path.join(pwDir, "index.js"));
const { chromium } = require(pwDir);
const password = "Kuma-E2E-FU-b8Nq3Rx6";

// Run it without HTTP_PROXY and HTTPS_PROXY set: Chromium takes them, and a proxy can't reach the plain address
const browser = await chromium.launch({ executablePath: chromiumPath, args: ["--no-proxy-server"] });
const options = { baseURL, viewport: { width: 1400, height: 1000 }, timezoneId: "UTC" };
const context = await browser.newContext(options);
const page = await context.newPage();
const errors = [];
const watch = (p) => {
    p.on("pageerror", (e) => errors.push(e.message));
    p.on("console", (m) => m.type() === "error" && /TypeError|ReferenceError/.test(m.text()) && errors.push(m.text()));
};
watch(page);
const results = [];
let current = page;

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
        await current
            .screenshot({ path: path.join(shots, `fail-${name.replace(/\W+/g, "-")}.png`), fullPage: true })
            .catch(() => {});
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
 * Wait until an element shows or hides. Polls, as locator.waitFor missed Bootstrap's dialogs.
 * @param {object} locator Element
 * @param {boolean} visible Shown, or hidden
 * @returns {Promise<void>}
 * @throws {Error} After ten seconds
 */
async function waitShown(locator, visible = true) {
    for (let i = 0; i < 50; i++) {
        if ((await locator.isVisible()) === visible) {
            await new Promise((r) => setTimeout(r, 400));
            return;
        }
        await new Promise((r) => setTimeout(r, 200));
    }
    throw new Error(`not ${visible ? "shown" : "hidden"} after 10s`);
}

/**
 * Send a realtime event from a page, and return its answer
 * @param {object} p Page
 * @param {...any} args Event and arguments
 * @returns {Promise<object>} Answer
 */
const emit = (p, ...args) =>
    p.evaluate(
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
 * Log in with the form
 * @param {object} p Page
 * @param {string} username Account
 * @returns {Promise<void>}
 */
async function login(p, username) {
    await p.goto("/dashboard");
    await p.getByPlaceholder("Username").fill(username);
    await p.getByPlaceholder("Password").fill(password);
    await p.getByRole("button", { name: "Log in" }).click();
    await p.waitForFunction(
        () => document.querySelector("#app").__vue_app__._container._vnode.component.proxy.loggedIn
    );
    await p.getByText("Quick Stats").waitFor();
}

/**
 * Add a manual monitor with the form
 * @param {string} name Name
 * @param {string} type Type
 * @returns {Promise<number>} Its id
 */
async function addMonitor(name, type = "manual") {
    await page.goto("/add");
    await page.getByTestId("monitor-type-select").selectOption(type);
    await page.getByTestId("friendly-name-input").fill(name);
    await page.getByTestId("save-button").click();
    await page.waitForURL(/\/dashboard\/\d+/, { timeout: 15000 });
    return Number(page.url().split("/").pop());
}

/**
 * Change monitors through the realtime API, at once
 * @param {number[]} ids Monitors
 * @param {object} change Fields to set
 * @returns {Promise<void>}
 */
async function editMonitors(ids, change) {
    const res = await page.evaluate(
        async ([ids, change]) => {
            const socket = document.querySelector("#app").__vue_app__._container._vnode.component.proxy.getSocket();
            const call = (...args) => new Promise((resolve) => socket.emit(...args, resolve));
            const monitors = await Promise.all(ids.map((id) => call("getMonitor", id)));
            return Promise.all(monitors.map((r) => call("editMonitor", { ...r.monitor, ...change })));
        },
        [ids, change]
    );
    expect(
        res.every((r) => r.ok),
        JSON.stringify(res)
    );
}

const ids = {};

await step("set up a group of two, a tagged monitor and a viewer", async () => {
    const setup = await page.request.post("/api/setup", { data: { username: "admin", password } });
    expect((await setup.json()).ok === true, "setup failed");
    execSync(psqlCommand, {
        input: "INSERT INTO app_user (username, password_hash, role) SELECT 'viewer', password_hash, 'viewer' FROM app_user WHERE username = 'admin';",
    });
    await login(page, "admin");
    ids.group = await addMonitor("backend", "group");
    ids.a = await addMonitor("backend-a");
    ids.b = await addMonitor("backend-b");
    ids.tagged = await addMonitor("noisy");
    await editMonitors([ids.a, ids.b], { parent: ids.group });
    const tag = await emit(page, "addTag", { name: "flaky", color: "#7C3AED" });
    expect(tag.ok, JSON.stringify(tag));
    ids.tag = tag.tag.id;
    const linked = await emit(page, "addMonitorTag", ids.tag, ids.tagged, "");
    expect(linked.ok, JSON.stringify(linked));
    await editMonitors([ids.a, ids.b, ids.tagged], { manual_status: 1 });
});

await step("a save the server refuses keeps the form", async () => {
    await page.goto(`/edit/${ids.a}`);
    await page.waitForFunction(
        () => document.querySelector('[data-testid="friendly-name-input"]')?.value === "backend-a"
    );
    const tooLong = "x".repeat(151);
    await page.getByTestId("friendly-name-input").fill(tooLong);
    await page.locator("#description").fill("kept after a failure");
    await page.getByTestId("save-button").click();
    await page.getByText("name must be 1 to 150 characters").waitFor({ timeout: 10000 });
    await page.waitForTimeout(1500);
    const name = await page.getByTestId("friendly-name-input").inputValue();
    const description = await page.locator("#description").inputValue();
    expect(name === tooLong && description === "kept after a failure", `name ${name.length}, "${description}"`);
    // Still counted as changes: leaving asks
    let asked = false;
    page.once("dialog", (d) => {
        asked = true;
        d.accept();
    });
    await page.getByRole("link", { name: "Dashboard" }).click();
    await page.waitForURL(/\/dashboard$/);
    expect(asked, "leaving didn't ask");
});

await step("muting a tag mutes its monitors' pop-ups", async () => {
    await page.goto("/settings/notifications");
    const box = page.locator(`#mute-tag-${ids.tag}`);
    await box.check();
    await page.goto("/dashboard");
    await page.getByText("Quick Stats").waitFor();
    await page.waitForTimeout(1500);
    await page.evaluate(() => document.querySelectorAll(".Vue-Toastification__close-button").forEach((b) => b.click()));
    await page.waitForTimeout(800);
    await editMonitors([ids.tagged, ids.b], { manual_status: 0 });
    await page.waitForFunction(
        () => document.querySelector('[data-testid="quick-stat-down"] .num')?.textContent.trim() !== "0",
        null,
        { timeout: 20000 }
    );
    await page.waitForTimeout(2500);
    const texts = await page.locator(".Vue-Toastification__toast-body").allInnerTexts();
    expect(!texts.some((t) => t.includes("noisy")), `noisy shown: ${JSON.stringify(texts)}`);
    expect(
        texts.some((t) => t.includes("backend-b")),
        `backend-b not shown: ${JSON.stringify(texts)}`
    );
    // Unmute, for what follows
    await page.goto("/settings/notifications");
    await page.locator(`#mute-tag-${ids.tag}`).uncheck();
    return texts.join(" / ");
});

await step("a group in the events filter shows its monitors' events", async () => {
    await page.goto("/dashboard");
    await page.getByTestId("event-filter-monitor").selectOption({ label: "backend" });
    await page.waitForTimeout(1000);
    const names = (await page.locator("table tbody tr td.name-column").allInnerTexts()).map((s) => s.trim());
    expect(names.length > 0, "no events");
    expect(
        names.every((n) => n === "backend" || n.startsWith("backend-")),
        `names ${names}`
    );
    expect(names.includes("backend-b"), `backend-b missing: ${names}`);
    expect(!names.includes("noisy"), "noisy listed");
    await page.getByTestId("event-filter-clear").click();
    return [...new Set(names)].join(", ");
});

const viewerContext = await browser.newContext(options);
const viewer = await viewerContext.newPage();
watch(viewer);

await step("a viewer's palette has no admin actions or settings", async () => {
    current = viewer;
    await login(viewer, "viewer");
    await viewer.evaluate(() => document.activeElement?.blur());
    await viewer.keyboard.press("Control+k");
    await waitShown(viewer.getByTestId("command-palette-input"));
    const groups = await viewer.locator('[data-testid="command-palette"] .result').allInnerTexts();
    const flat = groups.map((g) => g.replace(/\s+/g, " ").trim());
    expect(
        !flat.some((g) => g.startsWith("Add New Monitor") || g.startsWith("Schedule Maintenance")),
        flat.join(" | ")
    );
    const settings = flat.filter((g) => g.endsWith("Settings"));
    expect(settings.length === 1 && settings[0].startsWith("Security"), `settings: ${settings}`);
    await viewer.keyboard.press("Escape");
    await waitShown(viewer.getByTestId("command-palette"), false);
    return flat.slice(0, 3).join(" | ");
});

await step("a viewer has no events menu, and n and e do nothing", async () => {
    expect((await viewer.getByTestId("events-menu").count()) === 0, "the menu is shown");
    await viewer.evaluate(() => document.activeElement?.blur());
    await viewer.keyboard.press("n");
    await viewer.waitForTimeout(500);
    expect(new URL(viewer.url()).pathname === "/dashboard", `n went to ${viewer.url()}`);
    await viewer.goto(`/dashboard/${ids.a}`);
    await viewer.getByTestId("mute-toasts").waitFor();
    await viewer.evaluate(() => document.activeElement?.blur());
    await viewer.keyboard.press("e");
    await viewer.waitForTimeout(500);
    expect(new URL(viewer.url()).pathname === `/dashboard/${ids.a}`, `e went to ${viewer.url()}`);
    await viewer.keyboard.press("Shift+?");
    const help = viewer.getByTestId("shortcuts-help");
    await waitShown(help);
    const labels = await help.locator("tbody td:last-child").allInnerTexts();
    expect(!labels.includes("Add a monitor") && !labels.includes("Edit the monitor shown"), labels.join(", "));
    await viewer.keyboard.press("Escape");
});

await step("a viewer gets the AI prompt for a failing monitor", async () => {
    await viewer.goto(`/dashboard/${ids.b}`);
    await viewer.getByTestId("ai-prompt").click();
    const text = viewer.getByTestId("ai-prompt-text");
    await waitShown(text);
    const prompt = await text.inputValue();
    expect(prompt.startsWith("# Investigate a failing monitor: backend / backend-b"), prompt.slice(0, 80));
});

await viewerContext.close();

await step("on a Mac, ⌘K opens the palette, and the list shows ⌘", async () => {
    const mac = await browser.newContext(options);
    await mac.addInitScript(() => Object.defineProperty(navigator, "platform", { get: () => "MacIntel" }));
    const p = await mac.newPage();
    current = p;
    watch(p);
    await login(p, "admin");
    await p.evaluate(() => document.activeElement?.blur());
    await p.keyboard.press("Meta+k");
    await waitShown(p.getByTestId("command-palette-input"));
    await p.keyboard.press("Meta+k");
    await waitShown(p.getByTestId("command-palette"), false);
    await p.keyboard.press("Shift+?");
    await waitShown(p.getByTestId("shortcuts-help"));
    const first = (await p.getByTestId("shortcuts-help").locator("tbody tr").first().innerText()).replace(/\s+/g, " ");
    await mac.close();
    expect(first.startsWith("⌘+K"), first);
    return first;
});

await step("on a phone, / opens the list and searches it", async () => {
    const phone = await browser.newContext({ ...options, viewport: { width: 390, height: 844 }, hasTouch: true });
    const p = await phone.newPage();
    current = p;
    watch(p);
    await login(p, "admin");
    await p.evaluate(() => document.activeElement?.blur());
    await p.keyboard.press("/");
    await p.waitForURL(/\/list$/);
    await p.waitForFunction(() => document.activeElement?.dataset.testid === "monitor-search", null, {
        timeout: 5000,
    });
    await p.keyboard.type("noi");
    await p.waitForTimeout(400);
    const shown = await p.getByTestId("monitor-list").locator("a.item").count();
    await phone.close();
    expect(shown === 1, `shown ${shown}`);
});

await step("over plain HTTP, Copy still copies the prompt", async () => {
    const plain = await browser.newContext({ ...options, baseURL: plainURL });
    const p = await plain.newPage();
    current = p;
    watch(p);
    await login(p, "admin");
    const secure = await p.evaluate(() => [window.isSecureContext, typeof navigator.clipboard]);
    expect(secure[0] === false && secure[1] === "undefined", `secure context ${secure}`);
    await p.goto(`/dashboard/${ids.b}`);
    await p.getByTestId("ai-prompt").click();
    const text = p.getByTestId("ai-prompt-text");
    await waitShown(text);
    const prompt = await text.inputValue();
    await p.getByTestId("ai-prompt-copy").click();
    await p.getByText("Copied").waitFor();
    // Read the clipboard back from a page where reading it is allowed
    await context.grantPermissions(["clipboard-read"], { origin: baseURL });
    const clip = await page.evaluate(() => navigator.clipboard.readText());
    await plain.close();
    expect(clip === prompt, `clipboard has ${clip.length} characters, the prompt ${prompt.length}`);
});

await browser.close();
const passed = results.filter(Boolean).length;
console.log(`${passed}/${results.length} passed; page errors: ${errors.length}`);
for (const e of errors.slice(0, 5)) {
    console.log("page error:", e);
}
process.exit(passed === results.length ? 0 : 1);
