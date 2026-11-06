// Phase 5c end to end: an account with two-factor login, a status page with an uploaded logo,
// an incident and a maintenance window, made the way the Node.js server stored them, imported
// with kuma-import, then used through the web UI.
//
// Usage:
//   node import-5c.mjs seed <repository root> <data dir> <mariadb command>
//     Fills a source database that Node.js's knex migrations created, using better-auth's own
//     password hashing and encryption from the repository's node_modules. The command runs SQL
//     from standard input, such as "docker exec -i kuma-go-test-mariadb mariadb -uroot -ptest src".
//     The logo goes to <data dir>/upload, like Node.js's data directory.
//   node import-5c.mjs check <playwright-core dir> <chromium> <base URL> <screenshot dir>
//     Logs in with the password and a code, and looks at the imported things.
import { createHmac } from "node:crypto";
import { execSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";
import { pathToFileURL } from "node:url";

export const authSecret = "e2e-auth-secret-from-db-config-0123456789";
const totpKey = "E2eKey5cabcdefghijklmnopqrstuvwx";
const password = "Kuma-Import-5c-Pass";
// A 1x1 PNG.
const logo = Buffer.from(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==",
    "base64"
);

const [mode, ...args] = process.argv.slice(2);

/**
 * Quote a value for MariaDB
 * @param {string} value Text
 * @returns {string} SQL literal
 */
const q = (value) => "'" + String(value).replace(/\\/g, "\\\\").replace(/'/g, "''") + "'";

if (mode === "seed") {
    const [repo, dataDir, mariadb] = args;
    const crypto = await import(pathToFileURL(path.join(repo, "node_modules/better-auth/dist/crypto/index.mjs")));
    const hash = await crypto.hashPassword(password);
    const secret = await crypto.symmetricEncrypt({ key: authSecret, data: totpKey });
    const sql = `
        INSERT INTO better_auth_user (id, name, email, emailVerified, createdAt, updatedAt, username, displayUsername, role, twoFactorEnabled)
            VALUES ('u1', 'admin', 'admin@noreply.uptime-kuma.internal', 0, NOW(), NOW(), 'admin', 'admin', 'admin', 1);
        INSERT INTO better_auth_account (id, userId, accountId, providerId, password, createdAt, updatedAt)
            VALUES ('a1', 'u1', 'u1', 'credential', ${q(hash)}, NOW(), NOW());
        INSERT INTO better_auth_twoFactor (id, secret, backupCodes, verified, userId) VALUES ('t1', ${q(secret)}, '', 1, 'u1');
        INSERT INTO setting (\`key\`, value, type) VALUES ('serverTimezone', '"Europe/Berlin"', 'general');
        INSERT INTO monitor (id, name, type, url, \`interval\`, active, maxretries, ignore_tls, upside_down, maxredirects,
            accepted_statuscodes_json, retry_interval, method, conditions, config)
            VALUES (1, 'Website', 'http', 'https://example.com', 60, 1, 0, 0, 0, 10, '["200-299"]', 60, 'GET', '[]', '{}');
        INSERT INTO status_page (id, slug, title, description, icon, theme, footer_text)
            VALUES (1, 'main', 'Main Status', 'Imported from Node.js', '/upload/logo1.png?t=1730000000000', 'light', 'Footer from Node.js');
        INSERT INTO \`group\` (id, name, status_page_id, public, active, weight) VALUES (1, 'Services', 1, 1, 1, 1);
        INSERT INTO monitor_group (monitor_id, group_id, weight, send_url) VALUES (1, 1, 1, 0);
        INSERT INTO incident (title, content, style, created_date, pin, active, status_page_id)
            VALUES ('Planned work', 'Imported incident', 'warning', UTC_TIMESTAMP(), 1, 1, 1);
        INSERT INTO maintenance (id, title, description, active, strategy, start_time, end_time, weekdays, days_of_month,
            interval_day, cron, timezone, duration)
            VALUES (1, 'Nightly backup', 'Imported window', 1, 'recurring-weekday', '01:00:00', '02:00:00', '[0,1,2,3,4,5,6]', '[]',
            1, '0 1 * * 0,1,2,3,4,5,6', 'SAME_AS_SERVER', 3600);
        INSERT INTO monitor_maintenance (monitor_id, maintenance_id) VALUES (1, 1);
        INSERT INTO maintenance_status_page (status_page_id, maintenance_id) VALUES (1, 1);
    `;
    execSync(mariadb, { input: sql, stdio: ["pipe", "inherit", "inherit"] });
    mkdirSync(path.join(dataDir, "upload"), { recursive: true });
    writeFileSync(path.join(dataDir, "upload", "logo1.png"), logo);
    console.log("seeded");
    process.exit(0);
}

if (mode !== "check") {
    console.error("usage: import-5c.mjs seed|check ...");
    process.exit(2);
}

const [pwDir, chromiumPath, baseURL, shots] = args;
const require = createRequire(path.join(pwDir, "index.js"));
const { chromium } = require(pwDir);

/**
 * The RFC 6238 code for now, with the key as better-auth used it: its text's bytes
 * @returns {string} 6 digits
 */
function code() {
    const msg = Buffer.alloc(8);
    msg.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 30000)));
    const sum = createHmac("sha1", Buffer.from(totpKey)).update(msg).digest();
    const offset = sum[sum.length - 1] & 15;
    return String((sum.readUInt32BE(offset) & 0x7fffffff) % 1e6).padStart(6, "0");
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

await step("no setup is offered", async () => {
    const res = await page.request.post("/api/setup", { data: { username: "someone", password: "Another-Pass-123x" } });
    const body = await res.json();
    if (body.ok !== false) {
        throw new Error(JSON.stringify(body));
    }
});

await step("log in with the imported password and two-factor key", async () => {
    await page.goto("/dashboard");
    await page.getByPlaceholder("Username").fill("admin");
    await page.getByPlaceholder("Password").fill(password);
    await page.getByRole("button", { name: "Log in" }).click();
    await page.getByPlaceholder("123456").waitFor();
    await page.getByPlaceholder("123456").fill(code());
    await page.getByRole("button", { name: "Log in" }).click();
    await page.waitForSelector("text=Website");
});

await step("the maintenance window", async () => {
    await page.goto("/maintenance");
    await page.waitForSelector("text=Nightly backup");
    await page.goto("/maintenance");
    const href = await page.locator('a[href^="/maintenance/edit/"]').first().getAttribute("href");
    await page.goto(href);
    const timezone = page.locator("#timezone");
    await timezone.waitFor();
    for (let i = 0; i < 20 && (await timezone.inputValue()) !== "Europe/Berlin"; i++) {
        await page.waitForTimeout(250);
    }
    if ((await timezone.inputValue()) !== "Europe/Berlin") {
        throw new Error(`timezone ${await timezone.inputValue()}`);
    }
    return `${href}, in Europe/Berlin`;
});

await step("the status page, its logo, incident and maintenance", async () => {
    await page.goto("/status/main");
    await page.waitForSelector("text=Main Status");
    await page.waitForSelector("text=Planned work");
    await page.waitForSelector("text=Website");
    await page.waitForSelector("text=Footer from Node.js");
    const src = await page.locator("img.logo").first().getAttribute("src");
    if (!/^\/upload\/logo\d+\.png/.test(src)) {
        throw new Error(`logo ${src}`);
    }
    const res = await page.request.get(src);
    const body = await res.body();
    if (res.status() !== 200 || body.compare(logo) !== 0) {
        throw new Error(`logo ${res.status()} ${body.length} bytes`);
    }
    await page.screenshot({ path: path.join(shots, "status-page.png"), fullPage: true });
    return src;
});

await browser.close();
const passed = results.filter(Boolean).length;
console.log(`${passed}/${results.length} passed; page errors: ${errors.length}`);
for (const e of errors.slice(0, 5)) {
    console.log("page error:", e);
}
process.exit(passed === results.length ? 0 : 1);
