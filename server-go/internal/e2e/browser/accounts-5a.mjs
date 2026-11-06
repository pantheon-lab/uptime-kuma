// Phase 5a through the web UI: the leaked password check at setup, turning two-factor login on,
// logging in with a code, adding a scoped API key and using it with the REST API, deleting the
// key, and turning two-factor login off.
//
// Usage: node accounts-5a.mjs <playwright-core dir> <chromium> <base URL> <screenshot dir>
//
// It needs a new database, and a server that can reach api.pwnedpasswords.com.
import { createHmac } from "node:crypto";
import { createRequire } from "node:module";
import path from "node:path";

const [pwDir, chromiumPath, baseURL, shots] = process.argv.slice(2);
const require = createRequire(path.join(pwDir, "index.js"));
const { chromium } = require(pwDir);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const password = "Kuma-E2E-8f4Q2xR9p";

/**
 * Decode base32 without padding
 * @param {string} text Base32 text
 * @returns {Buffer} Bytes
 */
function base32(text) {
    const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
    let bits = "";
    for (const ch of text.toUpperCase()) {
        bits += alphabet.indexOf(ch).toString(2).padStart(5, "0");
    }
    const bytes = [];
    for (let i = 0; i + 8 <= bits.length; i += 8) {
        bytes.push(parseInt(bits.slice(i, i + 8), 2));
    }
    return Buffer.from(bytes);
}

/**
 * The RFC 6238 code for a step
 * @param {Buffer} secret Key
 * @param {number} step Time step
 * @returns {string} 6 digits
 */
function totp(secret, step) {
    const msg = Buffer.alloc(8);
    msg.writeBigUInt64BE(BigInt(step));
    const sum = createHmac("sha1", secret).update(msg).digest();
    const offset = sum[sum.length - 1] & 15;
    return String((sum.readUInt32BE(offset) & 0x7fffffff) % 1e6).padStart(6, "0");
}

const currentStep = () => Math.floor(Date.now() / 30000);

/**
 * Wait for the next time step, so that a code isn't one already used
 * @param {number} after Step that was used
 * @returns {Promise<number>} The new step
 */
async function nextStep(after) {
    while (currentStep() <= after) {
        await sleep(500);
    }
    return currentStep();
}

const browser = await chromium.launch({ executablePath: chromiumPath });
const context = await browser.newContext({ baseURL, viewport: { width: 1400, height: 1100 }, timezoneId: "UTC" });
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

const sessionUser = async () => (await (await page.request.get("/api/auth/get-session")).json())?.user ?? null;

await step("setup refuses a leaked password", async () => {
    const res = await page.request.post("/api/setup", { data: { username: "admin", password: "password123" } });
    const body = await res.json();
    if (body.ok !== false || !/compromised/.test(body.msg)) {
        throw new Error(JSON.stringify(body));
    }
    return body.msg;
});

await step("setup and log in", async () => {
    const res = await page.request.post("/api/setup", { data: { username: "admin", password } });
    if ((await res.json()).ok !== true) {
        throw new Error("setup failed");
    }
    await page.goto("/dashboard");
    await page.getByPlaceholder("Username").fill("admin");
    await page.getByPlaceholder("Password").fill(password);
    await page.getByRole("button", { name: "Log in" }).click();
    await page.waitForSelector("text=Add New Monitor");
});

let secret;
let usedStep;
await step("turn two-factor login on", async () => {
    await page.goto("/settings/security");
    await page.getByRole("button", { name: "2FA Settings" }).click();
    const modal = page.locator(".modal.show");
    await modal.locator("#current-password").fill(password);
    await modal.getByRole("button", { name: "Enable 2FA" }).click();
    await modal.getByRole("button", { name: "Show URI" }).click();
    const uri = await modal.locator("p.text-break").innerText();
    secret = base32(new URL(uri).searchParams.get("secret"));
    usedStep = currentStep();
    await modal.locator('input[autocomplete="one-time-code"]').fill(totp(secret, usedStep));
    await modal.getByRole("button", { name: "Save" }).click();
    await page.locator(".modal.show").getByRole("button", { name: "Yes" }).click();
    await page.waitForSelector("text=2FA Enabled.");
    if ((await sessionUser())?.twoFactorEnabled !== true) {
        throw new Error("the session doesn't say it's on");
    }
    return uri.replace(/secret=[^&]+/, "secret=…");
});

await step("log in with a code", async () => {
    await context.clearCookies();
    await page.goto("/dashboard");
    await page.getByPlaceholder("Username").fill("admin");
    await page.getByPlaceholder("Password").fill(password);
    await page.getByRole("button", { name: "Log in" }).click();
    await page.getByPlaceholder("123456").waitFor();
    if (await sessionUser()) {
        throw new Error("logged in before the code");
    }
    await page.getByPlaceholder("123456").fill("000000");
    await page.getByRole("button", { name: "Log in" }).click();
    await page.waitForSelector("text=Invalid code");
    usedStep = await nextStep(usedStep);
    await page.getByPlaceholder("123456").fill(totp(secret, usedStep));
    await page.getByRole("button", { name: "Log in" }).click();
    await page.waitForSelector("text=Add New Monitor");
    if ((await sessionUser())?.username !== "admin") {
        throw new Error("no session after the code");
    }
});

let key;
await step("add a read-only API key", async () => {
    await page.goto("/settings/api-keys");
    await page.getByRole("button", { name: "Add API Key" }).click();
    const modal = page.locator(".modal.show");
    await modal.locator("#name").fill("Read only");
    await modal.locator("#no-expire").check();
    for (const scope of ["monitors:write", "beats:read", "beats:write", "notifications:read", "notifications:write"]) {
        await modal.locator(`[id="api-key-scope-${scope}"]`).uncheck();
    }
    await modal.getByRole("button", { name: "Generate" }).click();
    await page.waitForSelector("text=Key Added");
    key = await page.locator(".modal.show input").inputValue();
    if (!key.startsWith("kuma_")) {
        throw new Error(`key ${key}`);
    }
    await page.locator(".modal.show").getByRole("button", { name: "Continue" }).click();
    await page.waitForSelector("text=Read only");
    await page.waitForSelector("text=monitors:read");
});

await step("the key works with its scope only", async () => {
    const auth = { Authorization: `Bearer ${key}` };
    const read = await page.request.get("/api/v1/monitors", { headers: auth });
    const write = await page.request.post("/api/v1/monitors", { headers: auth, data: { name: "x", type: "http" } });
    if (read.status() !== 200 || write.status() !== 403) {
        throw new Error(`read ${read.status()}, write ${write.status()}`);
    }
    return `read ${read.status()}, write ${write.status()}`;
});

await step("deleting the key revokes it", async () => {
    await page.locator(".item", { hasText: "Read only" }).getByRole("button", { name: "Delete" }).click();
    await page.locator(".modal.show").getByRole("button", { name: "Yes" }).click();
    await page.waitForSelector("text=No API Keys");
    const read = await page.request.get("/api/v1/monitors", { headers: { Authorization: `Bearer ${key}` } });
    if (read.status() !== 401) {
        throw new Error(`read after delete ${read.status()}`);
    }
});

await step("turn two-factor login off", async () => {
    await page.goto("/settings/security");
    await page.getByRole("button", { name: "2FA Settings" }).click();
    const modal = page.locator(".modal.show");
    await modal.getByText("Active").waitFor();
    await modal.locator("#current-password").fill(password);
    await modal.getByRole("button", { name: "Disable 2FA" }).click();
    await page.locator(".modal.show").getByRole("button", { name: "Yes" }).click();
    await page.waitForSelector("text=2FA Disabled.");
    if ((await sessionUser())?.twoFactorEnabled !== false) {
        throw new Error("the session says it's still on");
    }
    await context.clearCookies();
    const res = await page.request.post("/api/auth/sign-in/username", { data: { username: "admin", password } });
    if ((await res.json()).twoFactorRedirect) {
        throw new Error("a login still asks for a code");
    }
});

await page.goto("/settings/security");
await sleep(1000);
await page.screenshot({ path: path.join(shots, "security.png"), fullPage: true });
await browser.close();
const passed = results.filter(Boolean).length;
console.log(`${passed}/${results.length} passed; page errors: ${errors.length}`);
for (const e of errors.slice(0, 5)) {
    console.log("page error:", e);
}
process.exit(passed === results.length ? 0 : 1);
