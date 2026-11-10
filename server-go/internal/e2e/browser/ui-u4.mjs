// U4 against the built image: the Edit Monitor form's summary, its section links, the folded
// Advanced section, and a field the browser won't submit inside it.
//
// Usage: node ui-u4.mjs <playwright-core dir> <chromium> <base URL> <screenshot dir>
//
// It needs a new database. edit-monitor-snapshot.mjs checks that every type's fields are as before.
import { createRequire } from "node:module";
import path from "node:path";

const [pwDir, chromiumPath, baseURL, shots] = process.argv.slice(2);
const require = createRequire(path.join(pwDir, "index.js"));
const { chromium } = require(pwDir);
const password = "Kuma-E2E-U4-n2Hc7Lp4";

const browser = await chromium.launch({ executablePath: chromiumPath });
const context = await browser.newContext({ baseURL, viewport: { width: 1400, height: 900 }, timezoneId: "UTC" });
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

const summary = async () => (await page.getByTestId("monitor-summary").innerText()).replace(/\s+/g, " ").trim();
const sections = async () =>
    (await page.getByTestId("form-section-nav").locator("button").allInnerTexts()).map((s) => s.trim());

await step("set up an account and a notification", async () => {
    const setup = await page.request.post("/api/setup", { data: { username: "admin", password } });
    expect((await setup.json()).ok === true, "setup failed");
    await page.goto("/dashboard");
    await page.getByPlaceholder("Username").fill("admin");
    await page.getByPlaceholder("Password").fill(password);
    await page.getByRole("button", { name: "Log in" }).click();
    await page.getByText("Quick Stats").waitFor();
    const res = await page.evaluate(
        () =>
            new Promise((resolve) =>
                document
                    .querySelector("#app")
                    .__vue_app__._container._vnode.component.proxy.getSocket()
                    .emit(
                        "addNotification",
                        { name: "Team hook", type: "webhook", webhookURL: "http://127.0.0.1:1/hook", isDefault: false },
                        null,
                        resolve
                    )
            )
    );
    expect(res.ok, JSON.stringify(res));
});

await step("the summary follows the form", async () => {
    await page.goto("/add");
    await page.getByTestId("monitor-type-select").selectOption("keyword");
    await page.getByTestId("url-input").fill("https://shop.example.com/health");
    await page.locator("#keyword").fill("ok");
    const first = await summary();
    expect(
        first.includes("Requests GET https://shop.example.com/health and expects “ok” in the response every 1 minute"),
        first
    );
    expect(first.includes("goes down at the first failed check") && first.includes("It alerts no one"), first);
    await page.locator("#maxRetries").fill("2");
    await page.getByLabel("Team hook").check();
    const second = await summary();
    expect(
        second.includes("goes down after 3 failed checks in a row") && second.includes("It alerts Team hook."),
        second
    );
    return second;
});

await step("the section links follow the type, and jump", async () => {
    const http = await sections();
    expect(
        JSON.stringify(http) === JSON.stringify(["General", "Advanced", "Notifications", "Proxy", "HTTP Options"]),
        `keyword: ${http}`
    );
    await page.locator('[data-testid="form-section-nav"] button', { hasText: "HTTP Options" }).click();
    await page.waitForTimeout(900);
    const top = await page.locator("h2", { hasText: "HTTP Options" }).evaluate((h) => h.getBoundingClientRect().top);
    expect(top > 0 && top < 150, `the heading is at ${top}px`);
    const active = await page.locator('[data-testid="form-section-nav"] button.active').innerText();
    expect(active.trim() === "HTTP Options", `active ${active}`);
    await page.getByTestId("monitor-type-select").selectOption("dns");
    await page.waitForTimeout(300);
    const dns = await sections();
    expect(!dns.includes("HTTP Options") && dns.includes("General"), `dns: ${dns}`);
    return `${http.join(", ")} / dns: ${dns.join(", ")}`;
});

await step("Advanced is folded, and stays open once opened", async () => {
    await page.goto("/add");
    const toggle = page.getByTestId("advanced-toggle");
    expect((await toggle.getAttribute("aria-expanded")) === "false", "open at first");
    expect(!(await page.locator("#upside-down").isVisible()), "upside down shown while folded");
    await toggle.click();
    expect(await page.locator("#upside-down").isVisible(), "upside down hidden after opening");
    await page.reload();
    await page.getByTestId("advanced-toggle").waitFor();
    expect((await page.getByTestId("advanced-toggle").getAttribute("aria-expanded")) === "true", "folded after reload");
    await page.locator("#upside-down").check();
    const text = await summary();
    expect(text.includes("Upside down mode is on"), text);
});

await step("a bad value in folded Advanced opens it, and isn't saved", async () => {
    await page.goto("/add");
    await page.getByTestId("monitor-type-select").selectOption("http");
    await page.getByTestId("friendly-name-input").fill("bad redirects");
    await page.getByTestId("url-input").fill("https://example.com");
    await page.locator("#maxRedirects").fill("-1");
    // Fold it, whatever the earlier step left open
    const toggle = page.getByTestId("advanced-toggle");
    if ((await toggle.getAttribute("aria-expanded")) === "true") {
        await toggle.click();
    }
    expect(!(await page.locator("#maxRedirects").isVisible()), "still shown after folding");
    await page.getByTestId("save-button").click();
    await page.waitForTimeout(800);
    expect(await page.locator("#maxRedirects").isVisible(), "Advanced stayed folded");
    expect(new URL(page.url()).pathname === "/add", `saved and went to ${page.url()}`);
    const valid = await page.locator("#maxRedirects").evaluate((el) => el.validity.valid);
    expect(!valid, "the field counts as valid");
});

await step("Websocket Upgrade is listed once", async () => {
    const count = await page.locator('[data-testid="monitor-type-select"] option[value="websocket-upgrade"]').count();
    expect(count === 1, `${count} times`);
});

await page.goto("/add");
await page.screenshot({ path: path.join(shots, "u4-form.png") });
await browser.close();
const passed = results.filter(Boolean).length;
console.log(`${passed}/${results.length} passed; page errors: ${errors.length}`);
for (const e of errors.slice(0, 5)) {
    console.log("page error:", e);
}
process.exit(passed === results.length ? 0 : 1);
