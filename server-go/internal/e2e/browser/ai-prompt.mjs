// The prompt for an AI agent against the built image: a failing monitor's page and the dashboard
// offer it, a monitor that's up doesn't, and the prompt has the failure, the commands and the trace
// id, and none of the monitor's secrets.
//
// Usage: node ai-prompt.mjs <playwright-core dir> <chromium> <base URL> <screenshot dir>
//
// It needs a new database.
import { createRequire } from "node:module";
import { writeFileSync } from "node:fs";
import path from "node:path";

const [pwDir, chromiumPath, baseURL, shots] = process.argv.slice(2);
const require = createRequire(path.join(pwDir, "index.js"));
const { chromium } = require(pwDir);
const password = "Kuma-E2E-AI-r5Kp2Wd7";
const secrets = ["URLPASS-x9", "QUERYTOKEN-x9", "HEADER-x9", "BASIC-x9", "BODY-x9"];

const browser = await chromium.launch({ executablePath: chromiumPath });
const context = await browser.newContext({ baseURL, viewport: { width: 1400, height: 1000 }, timezoneId: "UTC" });
await context.grantPermissions(["clipboard-read", "clipboard-write"]);
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
 * Wait until an element shows. Polls, as locator.waitFor missed Bootstrap's dialogs opening.
 * @param {object} locator Element
 * @returns {Promise<void>}
 * @throws {Error} After ten seconds
 */
async function waitShown(locator) {
    for (let i = 0; i < 50; i++) {
        if (await locator.isVisible()) {
            await new Promise((r) => setTimeout(r, 400));
            return;
        }
        await new Promise((r) => setTimeout(r, 200));
    }
    throw new Error("not shown after 10s");
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
 * Add a monitor with the form
 * @param {string} type Type
 * @param {string} name Name
 * @param {Function} fill Fills the rest
 * @returns {Promise<string>} Its id
 */
async function addMonitor(type, name, fill) {
    await page.goto("/add");
    await page.getByTestId("monitor-type-select").selectOption(type);
    await page.getByTestId("friendly-name-input").fill(name);
    await fill?.();
    await page.getByTestId("save-button").click();
    await page.waitForURL(/\/dashboard\/\d+/, { timeout: 15000 });
    return page.url().split("/").pop();
}

const ids = {};

await step("set up a failing HTTP monitor with secrets, a port monitor and one that's up", async () => {
    const setup = await page.request.post("/api/setup", { data: { username: "admin", password } });
    expect((await setup.json()).ok === true, "setup failed");
    await page.goto("/dashboard");
    await page.getByPlaceholder("Username").fill("admin");
    await page.getByPlaceholder("Password").fill(password);
    await page.getByRole("button", { name: "Log in" }).click();
    await page.getByText("Quick Stats").waitFor();

    ids.api = await addMonitor("http", "api-down", () =>
        page.getByTestId("url-input").fill("http://bob:URLPASS-x9@127.0.0.1:1/health?token=QUERYTOKEN-x9&region=eu")
    );
    const got = await emit("getMonitor", Number(ids.api));
    const edited = await emit("editMonitor", {
        ...got.monitor,
        headers: '{"Authorization": "Bearer HEADER-x9"}',
        body: '{"secret": "BODY-x9"}',
        basic_auth_user: "bob",
        basic_auth_pass: "BASIC-x9",
    });
    expect(edited.ok, JSON.stringify(edited));
    ids.port = await addMonitor("port", "db-port", async () => {
        await page.locator("#hostname").fill("127.0.0.1");
        await page.locator("#port").fill("1");
    });
    ids.ok = await addMonitor("manual", "fine");
    const fine = await emit("getMonitor", Number(ids.ok));
    expect((await emit("editMonitor", { ...fine.monitor, manual_status: 1 })).ok, "manual up");
});

await step("a monitor that's up doesn't offer the prompt", async () => {
    await page.goto(`/dashboard/${ids.ok}`);
    await page.getByTestId("mute-toasts").waitFor();
    await page.waitForTimeout(3000);
    expect((await page.getByTestId("ai-prompt").count()) === 0, "offered");
});

let prompt = "";

await step("a failing monitor's page offers the prompt", async () => {
    await page.goto(`/dashboard/${ids.api}`);
    await page.getByTestId("ai-prompt").waitFor({ timeout: 30000 });
    await page.getByTestId("ai-prompt").click();
    const text = page.getByTestId("ai-prompt-text");
    await waitShown(text);
    prompt = await text.inputValue();
    writeFileSync(path.join(shots, "ai-prompt.md"), prompt);
    expect(prompt.startsWith("# Investigate a failing monitor: api-down"), prompt.slice(0, 80));
    return `${prompt.length} characters`;
});

await step("the prompt has the failure, the commands and the trace", async () => {
    for (const part of [
        "- Type: http",
        "- Status: DOWN, DOWN since",
        "connection refused",
        "| DOWN |",
        "```sh\ncurl -sS",
        "'http://127.0.0.1:1/health?token=REDACTED&region=eu'",
        "region=eu",
        "Authorization (values withheld)",
        `/dashboard/${ids.api}`,
    ]) {
        expect(prompt.includes(part), `missing ${JSON.stringify(part)}`);
    }
    const trace = prompt.match(/Trace ID of the latest failed check: `([0-9a-f]{32})`/);
    return trace ? `trace ${trace[1]}` : "no trace id (tracing off)";
});

await step("the prompt has none of the monitor's secrets", async () => {
    for (const secret of secrets) {
        expect(!prompt.includes(secret), `${secret} leaked`);
    }
});

await step("Copy puts it on the clipboard", async () => {
    await page.getByTestId("ai-prompt-copy").click();
    await page.getByText("Copied").waitFor();
    const clip = await page.evaluate(() => navigator.clipboard.readText());
    expect(clip === prompt, "the clipboard differs");
});

await step("Download saves it as Markdown", async () => {
    const [download] = await Promise.all([
        page.waitForEvent("download"),
        page.getByTestId("ai-prompt-download").click(),
    ]);
    expect(download.suggestedFilename() === "api-down-failure.md", download.suggestedFilename());
    await page.keyboard.press("Escape");
});

await step("the dashboard offers one prompt for every monitor down", async () => {
    await page.goto("/dashboard");
    const banner = page.getByTestId("down-banner");
    await banner.waitFor({ timeout: 30000 });
    const text = await banner.innerText();
    expect(text.includes("2 monitors are down"), text);
    await page.getByTestId("ai-prompt-all").click();
    const area = page.getByTestId("ai-prompt-text");
    await waitShown(area);
    const all = await area.inputValue();
    expect(all.startsWith("# Investigate 2 failing monitors"), all.slice(0, 80));
    expect(all.includes("## api-down") && all.includes("## db-port"), "a monitor is missing");
    expect(all.includes("nc -vz -w"), "no port command");
    expect(!all.includes("## fine"), "the monitor that's up is in it");
    for (const secret of secrets) {
        expect(!all.includes(secret), `${secret} leaked`);
    }
    await page.screenshot({ path: path.join(shots, "ai-prompt-dialog.png") });
    await page.keyboard.press("Escape");
});

await browser.close();
const passed = results.filter(Boolean).length;
console.log(`${passed}/${results.length} passed; page errors: ${errors.length}`);
for (const e of errors.slice(0, 5)) {
    console.log("page error:", e);
}
process.exit(passed === results.length ? 0 : 1);
