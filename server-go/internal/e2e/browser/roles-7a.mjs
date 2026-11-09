// Phase 7a against the built image: a viewer sees monitors, beats and maintenance windows, but no
// credentials, no way to change anything, and no settings; the server refuses changes anyway.
//
// Usage: node roles-7a.mjs <playwright-core dir> <chromium> <base URL> <screenshot dir> <admin token> <psql command>
//
// It needs a new database. The psql command runs SQL in it from standard input, and makes the
// viewer, as GitHub logins will in Phase 7b.
import { execSync } from "node:child_process";
import { createRequire } from "node:module";
import path from "node:path";

const [pwDir, chromiumPath, baseURL, shots, adminToken, psqlCommand] = process.argv.slice(2);
const require = createRequire(path.join(pwDir, "index.js"));
const { chromium } = require(pwDir);
const password = "Kuma-E2E-7a-p5Lw2Rx8";
const secrets = ["s3cret-pass", "hdr-t0ken", "qry-t0ken"];

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
 * Log in with the form
 * @param {string} username Account
 * @returns {Promise<void>}
 */
async function login(username) {
    await context.clearCookies();
    await page.goto("/dashboard");
    await page.getByPlaceholder("Username").fill(username);
    await page.getByPlaceholder("Password").fill(password);
    await page.getByRole("button", { name: "Log in" }).click();
    await page.waitForSelector("text=Secret site");
}

/**
 * Everything the page holds that could carry a credential, as JSON
 * @returns {Promise<string>} State
 */
const held = () =>
    page.evaluate(() => {
        const r = document.querySelector("#app").__vue_app__._container._vnode.component.proxy;
        return JSON.stringify([r.monitorList, r.notificationList, r.proxyList, r.dockerHostList, r.remoteBrowserList]);
    });

/**
 * The root component's state
 * @param {string} expression Read from the root, such as "role"
 * @returns {Promise<any>} Value
 */
const root = (expression) =>
    page.evaluate((e) => document.querySelector("#app").__vue_app__._container._vnode.component.proxy[e], expression);

let monitorID;
await step("set up an admin, a monitor with credentials, and a viewer", async () => {
    const setup = await page.request.post("/api/setup", { data: { username: "admin", password } });
    if ((await setup.json()).ok !== true) {
        throw new Error("setup failed");
    }
    const monitor = await page.request.post("/api/v1/monitors", {
        headers: { Authorization: `Bearer ${adminToken}` },
        data: {
            name: "Secret site",
            type: "http",
            url: `https://user:${secrets[0]}@example.com/health?token=${secrets[2]}`,
            headers: { Authorization: secrets[1] },
            interval: 60,
            active: false,
        },
    });
    if (monitor.status() !== 201) {
        throw new Error(`monitor ${monitor.status()} ${await monitor.text()}`);
    }
    monitorID = (await monitor.json()).id;
    execSync(psqlCommand, {
        input: "INSERT INTO app_user (username, password_hash, role) SELECT 'viewer', password_hash, 'viewer' FROM app_user WHERE username = 'admin';",
    });
});

await step("the admin still sees and can do everything", async () => {
    await login("admin");
    if ((await root("role")) !== "admin" || (await page.getByRole("link", { name: "Add New Monitor" }).count()) !== 1) {
        throw new Error("the admin's page is missing something");
    }
    const state = await held();
    if (!secrets.every((s) => state.includes(s))) {
        throw new Error("the admin wasn't sent the monitor's settings, so the viewer check would prove nothing");
    }
});

await step("a viewer sees the monitor without its credentials", async () => {
    await login("viewer");
    await page.goto(`/dashboard/${monitorID}`);
    await page.waitForSelector("text=https://example.com/health");
    const state = await held();
    const leaked = secrets.filter((s) => state.includes(s));
    if ((await root("role")) !== "viewer" || leaked.length > 0) {
        throw new Error(`role ${await root("role")}, leaked ${leaked}`);
    }
    await page.screenshot({ path: path.join(shots, "viewer-details.png") });
});

await step("a viewer has no buttons that change things", async () => {
    for (const [where, name] of [
        ["/dashboard", "Add New Monitor"],
        [`/dashboard/${monitorID}`, "Edit"],
        [`/dashboard/${monitorID}`, "Delete"],
        [`/dashboard/${monitorID}`, "Clear Data"],
        ["/maintenance", "Schedule Maintenance"],
        ["/manage-status-page", "New Status Page"],
    ]) {
        await page.goto(where);
        await page.waitForTimeout(500);
        if ((await page.getByRole("link", { name }).count()) + (await page.getByRole("button", { name }).count()) > 0) {
            throw new Error(`${name} on ${where}`);
        }
    }
    // Settings has only the viewer's own Security page (E2)
    await page.goto("/settings/security");
    await page.waitForTimeout(500);
    const menu = await page.locator(".settings-menu a").allInnerTexts();
    if (menu.map((t) => t.trim()).join(",") !== "Security") {
        throw new Error(`settings menu: ${menu}`);
    }
});

await step("a viewer is sent back from editing pages", async () => {
    const landed = [];
    // To the nearest page that only shows (U7)
    for (const [where, to] of [
        ["/settings/general", /\/settings\/security$/],
        ["/add", /\/dashboard$/],
        [`/edit/${monitorID}`, new RegExp(`/dashboard/${monitorID}$`)],
        ["/add-maintenance", /\/maintenance$/],
    ]) {
        await page.goto(where);
        await page.waitForURL(to, { timeout: 5000 });
        landed.push(where);
    }
    return landed.join(", ");
});

await step("the server refuses a viewer's changes", async () => {
    const answer = await page.evaluate(
        (id) =>
            new Promise((resolve) =>
                document
                    .querySelector("#app")
                    .__vue_app__._container._vnode.component.proxy.getSocket()
                    .emit("deleteMonitor", id, false, resolve)
            ),
        monitorID
    );
    if (answer.ok !== false || !/can only view/.test(answer.msg)) {
        throw new Error(JSON.stringify(answer));
    }
    const still = await page.request.get(`/api/v1/monitors/${monitorID}`, {
        headers: { Authorization: `Bearer ${adminToken}` },
    });
    if (still.status() !== 200) {
        throw new Error(`the monitor is gone: ${still.status()}`);
    }
    return answer.msg;
});

await browser.close();
const passed = results.filter(Boolean).length;
console.log(`${passed}/${results.length} passed; page errors: ${errors.length}`);
for (const e of errors.slice(0, 5)) {
    console.log("page error:", e);
}
process.exit(passed === results.length ? 0 : 1);
