// U3 against the built image: the keyboard shortcuts and the command palette.
//
// Usage: node ui-u3.mjs <playwright-core dir> <chromium> <base URL> <screenshot dir>
//
// It needs a new database.
import { createRequire } from "node:module";
import path from "node:path";

const [pwDir, chromiumPath, baseURL, shots] = process.argv.slice(2);
const require = createRequire(path.join(pwDir, "index.js"));
const { chromium } = require(pwDir);
const password = "Kuma-E2E-U3-h6Td4Mz8";

const browser = await chromium.launch({ executablePath: chromiumPath });
const context = await browser.newContext({ baseURL, viewport: { width: 1400, height: 1000 }, timezoneId: "UTC" });
const page = await context.newPage();
const errors = [];
page.on("pageerror", (e) => errors.push(e.message));
page.on("console", (m) => m.type() === "error" && /TypeError|ReferenceError/.test(m.text()) && errors.push(m.text()));
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
 * Press keys with nothing focused
 * @param {...string} keys Keys, one after the other
 * @returns {Promise<void>}
 */
async function press(...keys) {
    await page.evaluate(() => document.activeElement?.blur());
    for (const key of keys) {
        await page.keyboard.press(key);
    }
}

/**
 * The path shown
 * @returns {string} Path
 */
const currentPath = () => new URL(page.url()).pathname;

/**
 * Wait until a dialog's element shows or hides. Polls, as locator.waitFor missed Bootstrap's
 * dialogs opening in headless Chromium.
 * @param {object} locator Element
 * @param {boolean} visible Shown, or hidden
 * @returns {Promise<void>}
 * @throws {Error} After ten seconds
 */
async function waitShown(locator, visible = true) {
    for (let i = 0; i < 50; i++) {
        if ((await locator.isVisible()) === visible) {
            // Let Bootstrap's fade end, after which the dialog has the keyboard
            await new Promise((r) => setTimeout(r, 400));
            return;
        }
        await new Promise((r) => setTimeout(r, 200));
    }
    throw new Error(`not ${visible ? "shown" : "hidden"} after 10s`);
}

const ids = {};

await step("set up three monitors", async () => {
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
});

await step("? lists the shortcuts", async () => {
    await page.goto("/dashboard");
    await page.getByText("Quick Stats").waitFor();
    await press("Shift+?");
    const help = page.getByTestId("shortcuts-help");
    await waitShown(help.getByText("Go to anything"));
    const rows = await help.locator("tbody tr").count();
    await page.keyboard.press("Escape");
    await waitShown(help, false);
    return `${rows} shortcuts`;
});

await step("/ searches the list, and Esc clears, then leaves it", async () => {
    await press("/");
    const focused = await page.evaluate(() => document.activeElement?.dataset.testid);
    expect(focused === "monitor-search", `focused ${focused}`);
    await page.keyboard.type("bra");
    expect(currentPath() === "/dashboard", "typing went to shortcuts");
    await page.waitForTimeout(300);
    const shown = await page.getByTestId("monitor-list").locator("a.item").count();
    expect(shown === 1, `shown ${shown}`);
    await page.keyboard.press("Escape");
    expect((await page.getByTestId("monitor-search").inputValue()) === "", "not cleared");
    await page.keyboard.press("Escape");
    const after = await page.evaluate(() => document.activeElement?.dataset.testid ?? null);
    expect(after !== "monitor-search", "still focused");
});

await step("j and k move through the list", async () => {
    await press("j");
    await page.waitForURL(`**/dashboard/${ids.alpha}`);
    await press("j");
    await page.waitForURL(`**/dashboard/${ids.bravo}`);
    await press("k");
    await page.waitForURL(`**/dashboard/${ids.alpha}`);
    await press("k");
    await page.waitForTimeout(300);
    expect(currentPath() === `/dashboard/${ids.alpha}`, `the first moved to ${currentPath()}`);
});

await step("e edits the monitor shown, and g d goes to the dashboard", async () => {
    await press("e");
    await page.waitForURL(`**/edit/${ids.alpha}`);
    await page.getByTestId("friendly-name-input").waitFor();
    await page.waitForTimeout(500);
    await press("g", "d");
    await page.waitForURL("**/dashboard");
});

await step("n adds a monitor, and g s and g m go to their pages", async () => {
    await press("n");
    await page.waitForURL("**/add");
    await page.waitForTimeout(500);
    await press("g", "s");
    await page.waitForURL("**/manage-status-page");
    await press("g", "m");
    await page.waitForURL("**/maintenance");
});

await step("Ctrl+K goes to a monitor", async () => {
    await press("Control+k");
    const input = page.getByTestId("command-palette-input");
    await waitShown(input);
    await page.waitForFunction(() => document.activeElement?.dataset.testid === "command-palette-input");
    await page.keyboard.type("charl");
    const first = (await page.locator('[data-testid="command-palette"] .result.selected .label').innerText()).trim();
    expect(first === "charlie", `first ${first}`);
    await page.keyboard.press("Enter");
    await page.waitForURL(`**/dashboard/${ids.charlie}`);
    await waitShown(page.getByTestId("command-palette"), false);
});

await step("Ctrl+K finds a settings page, with the arrows", async () => {
    await press("Control+k");
    await waitShown(page.getByTestId("command-palette-input"));
    await page.waitForFunction(() => document.activeElement?.dataset.testid === "command-palette-input");
    await page.keyboard.type("se");
    const labels = await page.locator('[data-testid="command-palette"] .result .label').allInnerTexts();
    const target = labels.findIndex((l) => l.trim() === "Reverse Proxy");
    expect(target > 0, `results ${labels}`);
    for (let i = 0; i < target; i++) {
        await page.keyboard.press("ArrowDown");
    }
    await page.keyboard.press("Enter");
    await page.waitForURL("**/settings/reverse-proxy");
    return labels.slice(0, 4).join(", ");
});

await step("Ctrl+K again closes the palette, and Esc does too", async () => {
    await press("Control+k");
    await waitShown(page.getByTestId("command-palette-input"));
    await page.waitForTimeout(400);
    await page.keyboard.press("Control+k");
    await waitShown(page.getByTestId("command-palette"), false);
    await press("Control+k");
    await waitShown(page.getByTestId("command-palette-input"));
    await page.waitForTimeout(400);
    await page.keyboard.press("Escape");
    await waitShown(page.getByTestId("command-palette"), false);
});

await step("keys typed in a field aren't shortcuts", async () => {
    await page.goto(`/edit/${ids.bravo}`);
    await page.getByTestId("friendly-name-input").click();
    await page.keyboard.type(" n g d");
    await page.waitForTimeout(500);
    expect(currentPath() === `/edit/${ids.bravo}`, `went to ${currentPath()}`);
    const value = await page.getByTestId("friendly-name-input").inputValue();
    expect(value.endsWith(" n g d"), `typed ${value}`);
});

await step("the menu lists the shortcuts", async () => {
    await page.goto("/dashboard");
    await page.locator(".dropdown-profile-pic .nav-link").click();
    await page.getByTestId("menu-shortcuts").click();
    await waitShown(page.getByTestId("shortcuts-help").getByText("Go to anything"));
    await page.keyboard.press("Escape");
});

await step("no shortcuts before logging in", async () => {
    const other = await browser.newPage({ baseURL });
    await other.goto("/dashboard");
    await other.getByPlaceholder("Username").waitFor();
    await other.evaluate(() => document.activeElement?.blur());
    await other.keyboard.press("n");
    await other.keyboard.press("Control+k");
    await other.waitForTimeout(500);
    const path = new URL(other.url()).pathname;
    const palettes = await other.getByTestId("command-palette").isVisible();
    await other.close();
    expect(path === "/dashboard" && !palettes, `path ${path}, palette shown ${palettes}`);
});

await press("Control+k");
await waitShown(page.getByTestId("command-palette-input"));
await page.keyboard.type("a");
await page.waitForTimeout(300);
await page.screenshot({ path: path.join(shots, "u3-palette.png") });
await browser.close();
const passed = results.filter(Boolean).length;
console.log(`${passed}/${results.length} passed; page errors: ${errors.length}`);
for (const e of errors.slice(0, 5)) {
    console.log("page error:", e);
}
process.exit(passed === results.length ? 0 : 1);
