// E3 against the built image: stored secrets made without a key are encrypted at the first start
// with KUMA_SECRET_KEY, and the UI still shows and uses them; new ones are stored encrypted.
//
// Usage: node secrets-e3.mjs <playwright-core dir> <chromium> <base URL> <screenshot dir> <psql command> <before|after>
//
// Run "before" against the image started without KUMA_SECRET_KEY on a new database, restart it
// with the key, and run "after". The psql command reads the database from standard input.
import { execSync } from "node:child_process";
import { createRequire } from "node:module";
import path from "node:path";

const [pwDir, chromiumPath, baseURL, shots, psqlCommand, phase] = process.argv.slice(2);
const require = createRequire(path.join(pwDir, "index.js"));
const { chromium } = require(pwDir);
const password = "Kuma-E2E-E3-h6Vn2Lp8";

const browser = await chromium.launch({ executablePath: chromiumPath });
const context = await browser.newContext({ baseURL, viewport: { width: 1400, height: 1000 }, timezoneId: "UTC" });
const page = await context.newPage();
const errors = [];
page.on("pageerror", (e) => errors.push(e.message));
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
 * Run SQL and return its output
 * @param {string} sql Query
 * @returns {string} Output
 */
const sql = (sql) => execSync(psqlCommand + " -At", { input: sql }).toString();

/**
 * Log in with the form
 * @returns {Promise<void>}
 */
async function login() {
    await page.goto("/dashboard");
    await page.getByPlaceholder("Username").fill("admin");
    await page.getByPlaceholder("Password").fill(password);
    await page.getByRole("button", { name: "Log in" }).click();
    await page.waitForSelector("text=Add New Monitor");
}

/**
 * Add a notification with a webhook URL that's a secret
 * @param {string} name Name
 * @param {string} secret Part of the URL
 * @returns {Promise<void>}
 */
async function addNotification(name, secret) {
    const res = await emit(
        "addNotification",
        { name, type: "slack", slackwebhookURL: `https://hooks.example.com/${secret}`, isDefault: false },
        null
    );
    if (!res.ok) {
        throw new Error(JSON.stringify(res));
    }
}

if (phase === "before") {
    await step("without a key, secrets are stored as they are", async () => {
        const setup = await page.request.post("/api/setup", { data: { username: "admin", password } });
        if ((await setup.json()).ok !== true) {
            throw new Error("setup failed");
        }
        await login();
        await addNotification("Old Slack", "old-hook-s3cret");
        const proxy = await emit(
            "addProxy",
            {
                protocol: "http",
                host: "proxy.example",
                port: 3128,
                auth: true,
                username: "u",
                password: "old-proxy-s3cret",
            },
            null
        );
        if (!proxy.ok) {
            throw new Error(JSON.stringify(proxy));
        }
        const stored = sql("SELECT config::text FROM notification; SELECT password FROM proxy;");
        if (!stored.includes("old-hook-s3cret") || !stored.includes("old-proxy-s3cret")) {
            throw new Error(`not stored as they are: ${stored}`);
        }
    });
} else {
    await step("after a start with the key, they're encrypted", async () => {
        const stored = sql("SELECT config::text FROM notification; SELECT password FROM proxy;");
        if (stored.includes("s3cret") || (stored.match(/kenc:v1:/g) ?? []).length !== 2) {
            throw new Error(`stored: ${stored}`);
        }
        return stored.trim().split("\n")[0].slice(0, 30) + "…";
    });

    await step("the UI shows them, and adds new ones encrypted", async () => {
        await login();
        await page.goto("/settings/notifications");
        await page.locator("li", { hasText: "Old Slack" }).getByText("Edit").click();
        const url = await page.locator("#slack-webhook-url").inputValue();
        if (url !== "https://hooks.example.com/old-hook-s3cret") {
            throw new Error(`the dialog shows ${url}`);
        }
        await page.screenshot({ path: path.join(shots, "notification.png") });
        await page.keyboard.press("Escape");
        await page.goto("/settings/proxies");
        await page.getByText("proxy.example").first().waitFor();
        await addNotification("New Slack", "new-hook-s3cret");
        const stored = sql("SELECT config::text FROM notification ORDER BY id");
        if (stored.includes("s3cret") || (stored.match(/kenc:v1:/g) ?? []).length !== 2) {
            throw new Error(`stored: ${stored}`);
        }
    });
}

await browser.close();
const passed = results.filter(Boolean).length;
console.log(`${passed}/${results.length} passed; page errors: ${errors.length}`);
for (const e of errors.slice(0, 5)) {
    console.log("page error:", e);
}
process.exit(passed === results.length ? 0 : 1);
