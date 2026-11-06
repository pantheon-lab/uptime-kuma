// Phase 6c against a built image: an apprise notification tested through the REST API, delivered
// by the apprise command to a listener this script runs, and the Reverse Proxy page, which now
// explains running cloudflared beside the server.
//
// Usage: node apprise-tunnel-6c.mjs <playwright-core dir> <chromium> <base URL> <screenshot dir> <admin token> <listener port> <apprise|default>
//
// With "apprise", the image is the Dockerfile's apprise target and the message must arrive; with
// "default", the image has no apprise command, and the test must say so. It needs a new database.
import { createServer } from "node:http";
import { createRequire } from "node:module";
import path from "node:path";

const [pwDir, chromiumPath, baseURL, shots, adminToken, listenerPort, variant] = process.argv.slice(2);
const require = createRequire(path.join(pwDir, "index.js"));
const { chromium } = require(pwDir);
const password = "Kuma-E2E-6c-m3Tz9Ws1";

const received = [];
const listener = createServer((req, res) => {
    let body = "";
    req.on("data", (chunk) => (body += chunk));
    req.on("end", () => {
        received.push(body);
        res.end();
    });
});
await new Promise((resolve) => listener.listen(Number(listenerPort), "127.0.0.1", resolve));

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

const admin = { Authorization: `Bearer ${adminToken}` };

await step(`an apprise notification in the ${variant} image`, async () => {
    const created = await page.request.post("/api/v1/notifications", {
        headers: admin,
        data: { name: "Apprise", type: "apprise", config: { appriseURL: `json://127.0.0.1:${listenerPort}/hook` } },
    });
    const { id } = await created.json();
    const test = await page.request.post(`/api/v1/notifications/${id}/test`, { headers: admin });
    const body = await test.json();
    if (variant === "apprise") {
        if (test.status() !== 200 || received.length !== 1 || JSON.parse(received[0]).message !== "Apprise Testing") {
            throw new Error(`${test.status()} ${JSON.stringify(body)}, received ${JSON.stringify(received)}`);
        }
        return received[0];
    }
    if (body.ok !== false || !/apprise command isn't installed/.test(body.error) || received.length !== 0) {
        throw new Error(`${test.status()} ${JSON.stringify(body)}`);
    }
    return body.error;
});

await step("the Reverse Proxy page explains the cloudflared sidecar", async () => {
    const setup = await page.request.post("/api/setup", { data: { username: "admin", password } });
    if ((await setup.json()).ok !== true) {
        throw new Error("setup failed");
    }
    await page.goto("/dashboard");
    await page.getByPlaceholder("Username").fill("admin");
    await page.getByPlaceholder("Password").fill(password);
    await page.getByRole("button", { name: "Log in" }).click();
    await page.waitForSelector("text=Add New Monitor");
    await page.goto("/settings/reverse-proxy");
    const section = page.locator("#cloudflare-tunnel");
    await section.getByText("docker compose --profile tunnel up -d").waitFor();
    await section.getByText("cloudflared.enabled").waitFor();
    if ((await page.getByRole("button", { name: /Start cloudflared/ }).count()) !== 0) {
        throw new Error("the old start button is shown");
    }
    // The trust proxy setting on the same page still saves.
    await page.locator("#trustProxyYes").check();
    await page.getByRole("button", { name: "Save" }).click();
    await page.waitForSelector("text=Saved");
    await page.screenshot({ path: path.join(shots, "reverse-proxy.png"), fullPage: true });
});

await browser.close();
listener.close();
const passed = results.filter(Boolean).length;
console.log(`${passed}/${results.length} passed; page errors: ${errors.length}`);
for (const e of errors.slice(0, 5)) {
    console.log("page error:", e);
}
process.exit(passed === results.length ? 0 : 1);
