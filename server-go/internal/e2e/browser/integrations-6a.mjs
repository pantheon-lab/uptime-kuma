// Phase 6a against the built image: the MCP server at /mcp, spoken to as a client would over
// plain HTTP, and the update notice, with a fake list of tags.
//
// Usage: node integrations-6a.mjs <playwright-core dir> <chromium> <base URL> <screenshot dir> <admin token> <tags port>
//
// Start the image with KUMA_UPDATE_CHECK_URL=http://127.0.0.1:<tags port>/tags on a new database.
// This script serves the tags.
import { createServer } from "node:http";
import { createRequire } from "node:module";
import path from "node:path";

const [pwDir, chromiumPath, baseURL, shots, adminToken, tagsPort] = process.argv.slice(2);
const require = createRequire(path.join(pwDir, "index.js"));
const { chromium } = require(pwDir);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const password = "Kuma-E2E-6a-r7Hq2Lp9";

const tags = createServer((req, res) => {
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify([{ name: "v3.1.0-beta.1" }, { name: "v3.0.0" }, { name: "v2.9.0" }]));
});
await new Promise((resolve) => tags.listen(Number(tagsPort), "127.0.0.1", resolve));

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

/**
 * Send one JSON-RPC message to /mcp, as a Streamable HTTP client does
 * @param {string} key Bearer key, or empty
 * @param {string} method Method
 * @param {object} params Params
 * @returns {Promise<{status: number, body: object}>} Answer
 */
async function rpc(key, method, params = {}) {
    const headers = {
        "Content-Type": "application/json",
        Accept: "application/json, text/event-stream",
        "MCP-Protocol-Version": "2025-06-18",
    };
    if (key) {
        headers.Authorization = `Bearer ${key}`;
    }
    const res = await page.request.post("/mcp", { headers, data: { jsonrpc: "2.0", id: 1, method, params } });
    const text = await res.text();
    let body = null;
    try {
        body = JSON.parse(text);
    } catch {
        body = { raw: text };
    }
    return { status: res.status(), body };
}

let readKey;
await step("set up, with a monitor and a key that can only read monitors", async () => {
    const setup = await page.request.post("/api/setup", { data: { username: "admin", password } });
    if ((await setup.json()).ok !== true) {
        throw new Error("setup failed");
    }
    const monitor = await page.request.post("/api/v1/monitors", {
        headers: admin,
        data: { name: "Example site", type: "http", url: "https://example.com", interval: 60, active: false },
    });
    if (monitor.status() !== 201) {
        throw new Error(`monitor ${monitor.status()} ${await monitor.text()}`);
    }
    const key = await page.request.post("/api/v1/api-keys", {
        headers: admin,
        data: { name: "mcp", scopes: ["monitors:read"] },
    });
    const body = await key.json();
    readKey = body.key;
    if (key.status() !== 201 || !readKey?.startsWith("kuma_")) {
        throw new Error(`key ${key.status()} ${JSON.stringify(body)}`);
    }
});

await step("MCP refuses a request without a key", async () => {
    const { status } = await rpc("", "initialize", {
        protocolVersion: "2025-06-18",
        capabilities: {},
        clientInfo: { name: "e2e", version: "1" },
    });
    if (status !== 401) {
        throw new Error(`status ${status}`);
    }
});

await step("MCP initializes and lists the read-only tools", async () => {
    const init = await rpc(readKey, "initialize", {
        protocolVersion: "2025-06-18",
        capabilities: {},
        clientInfo: { name: "e2e", version: "1" },
    });
    if (init.status !== 200 || init.body.result?.serverInfo?.name !== "uptime-kuma") {
        throw new Error(`initialize ${init.status} ${JSON.stringify(init.body)}`);
    }
    const list = await rpc(readKey, "tools/list");
    const names = (list.body.result?.tools ?? []).map((t) => t.name);
    for (const want of ["listMonitors", "getMonitor", "getStats", "getBeats"]) {
        if (!names.includes(want)) {
            throw new Error(`no ${want} in ${names}`);
        }
    }
    if (names.includes("createMonitor") || names.includes("streamEvents")) {
        throw new Error(`unexpected tools: ${names}`);
    }
    return `${names.length} tools, server version ${init.body.result.serverInfo.version}`;
});

await step("an MCP tool answers with the API's data", async () => {
    const call = await rpc(readKey, "tools/call", { name: "listMonitors", arguments: { limit: 10 } });
    const result = call.body.result;
    if (call.status !== 200 || result?.isError || !result?.content?.[0]?.text?.includes("Example site")) {
        throw new Error(JSON.stringify(call.body).slice(0, 400));
    }
});

await step("an MCP tool needs the key's scope", async () => {
    const call = await rpc(readKey, "tools/call", { name: "getBeats", arguments: { id: 1 } });
    const result = call.body.result;
    if (!result?.isError || !result.content[0].text.includes("HTTP 403")) {
        throw new Error(JSON.stringify(call.body).slice(0, 400));
    }
    return result.content[0].text.split("\n")[0];
});

/**
 * The newest version the server told the page about
 * @returns {Promise<string|undefined>} Version
 */
const latest = () =>
    page.evaluate(
        () => document.querySelector("#app").__vue_app__._container._vnode.component.proxy.info.latestVersion
    );

/**
 * Tick or untick a setting on the About page, wait for the check it starts, and reload
 * @param {string} label Checkbox
 * @param {boolean} on Ticked
 * @returns {Promise<void>}
 */
async function setAndReload(label, on) {
    const box = page.getByLabel(label);
    await (on ? box.check() : box.uncheck());
    await page.waitForSelector("text=Saved");
    await sleep(1500);
    await page.reload();
    await page.waitForSelector("text=Also check beta release");
}

await step("the update notice follows the settings", async () => {
    await page.goto("/dashboard");
    await page.getByPlaceholder("Username").fill("admin");
    await page.getByPlaceholder("Password").fill(password);
    await page.getByRole("button", { name: "Log in" }).click();
    await page.waitForSelector("text=Example site");
    await page.goto("/settings/about");
    await page.waitForSelector("text=Also check beta release");
    const notice = page.getByRole("link", { name: "New Update" });

    // Turning checking off and on again checks now, with the tags this script serves.
    await setAndReload("Show update if available", false);
    await setAndReload("Show update if available", true);
    const stable = await latest();
    const shown = await notice.count();
    await setAndReload("Also check beta release", true);
    const withBeta = await latest();
    await setAndReload("Show update if available", false);
    const off = await latest();
    if (
        stable !== "3.0.0" ||
        shown !== 1 ||
        withBeta !== "3.1.0-beta.1" ||
        off !== undefined ||
        (await notice.count()) !== 0
    ) {
        throw new Error(`stable ${stable} (notice ${shown}), with betas ${withBeta}, off ${off}`);
    }
    await page.screenshot({ path: path.join(shots, "about.png") });
    return `${stable} with the notice, then ${withBeta} with betas, then none`;
});

await browser.close();
tags.close();
const passed = results.filter(Boolean).length;
console.log(`${passed}/${results.length} passed; page errors: ${errors.length}`);
for (const e of errors.slice(0, 5)) {
    console.log("page error:", e);
}
process.exit(passed === results.length ? 0 : 1);
