// Upstream's status page specs (test/e2e/specs at 921745bf^), ported to playwright-core and run
// against the built image, with a new database.
//
// Usage: node status-pages.mjs <playwright-core dir> <chromium binary> <base URL> <screenshot dir>
//
// Install playwright-core in a scratch directory, not here, and pass its path. The server must
// run in UTC, as the script's browser does.
import { createRequire } from "node:module";
import fs from "node:fs";
import path from "node:path";

const [pwDir, chromiumPath, baseURL, shots] = process.argv.slice(2);
const require = createRequire(path.join(pwDir, "index.js"));
const { chromium } = require(pwDir);

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
/**
 * Wait until fn returns true
 * @param {string} desc What is awaited, for the error
 * @param {Function} fn Check, which may throw while not ready
 * @param {number} timeout Milliseconds to wait
 * @returns {Promise<void>}
 */
async function until(desc, fn, timeout = 15000) {
    const end = Date.now() + timeout;
    let last;
    while (Date.now() < end) {
        try {
            if (await fn()) {
                return;
            }
        } catch (e) {
            last = e;
        }
        await sleep(200);
    }
    throw new Error(`timed out: ${desc}${last ? ` (${last.message})` : ""}`);
}
const visible = (loc) => until(`visible ${loc}`, () => loc.isVisible());
const count = (loc, n) => until(`${loc} has ${n}`, async () => (await loc.count()) === n);
const contains = (loc, text) =>
    until(`${loc} contains ${text}`, async () => (await loc.first().textContent()).includes(text));

const results = [];
/**
 * Run one spec, record its result, and take a screenshot if it fails
 * @param {string} name Spec name
 * @param {Function} fn Spec
 * @param {object} page Page to screenshot
 * @returns {Promise<void>}
 */
async function run(name, fn, page) {
    const started = Date.now();
    try {
        await fn();
        results.push({ name, ok: true, ms: Date.now() - started });
        console.log(`PASS ${name}`);
    } catch (e) {
        results.push({ name, ok: false, error: e.message });
        console.log(`FAIL ${name}: ${e.message}`);
        await page
            .screenshot({ path: path.join(shots, `fail-${name.replace(/\W+/g, "-")}.png`), fullPage: true })
            .catch(() => {});
    }
}

const PASSWORD = "Kuma-E2E-8f4Q2xR9p";
const browser = await chromium.launch({ executablePath: chromiumPath });
// The server runs in UTC, and the maintenance form fills in the browser's local time, which the
// server reads as its own, so the browser runs in UTC too, as upstream's CI did.
const context = await browser.newContext({ baseURL, viewport: { width: 1400, height: 1000 }, timezoneId: "UTC" });
const page = await context.newPage();
const consoleErrors = [];
page.on("pageerror", (e) => consoleErrors.push(e.message));

/**
 * Log in through the form, if it's shown
 * @returns {Promise<void>}
 */
async function login() {
    if (
        await page
            .getByPlaceholder("Username")
            .isVisible()
            .catch(() => false)
    ) {
        await page.getByPlaceholder("Username").fill("admin");
        await page.getByPlaceholder("Password").fill(PASSWORD);
        await page.getByLabel("Remember me").check();
        await page.getByRole("button", { name: "Log in" }).click();
    }
    await page.waitForSelector("text=Add New Monitor");
}

/**
 * Wait until the editor has the page's private config, which carries its id
 * @returns {Promise<void>}
 */
async function waitForConfigLoaded() {
    await until("config loaded", async () =>
        /^\d+$/.test((await page.getByTestId("edit-sidebar").getAttribute("data-status-page-id")) || "")
    );
}

/**
 * Open the editor and wait for its config
 * @returns {Promise<void>}
 */
async function openEditSidebar() {
    await page.getByTestId("edit-button").click();
    await waitForConfigLoaded();
}

/**
 * Add an HTTP monitor through the form
 * @param {string} name Monitor name
 * @param {string} url Monitor URL
 * @returns {Promise<string>} Monitor id
 */
async function addHttpMonitor(name, url) {
    await page.goto("/add");
    await login();
    await page.getByTestId("monitor-type-select").selectOption("http");
    await page.getByTestId("friendly-name-input").fill(name);
    await page.getByTestId("url-input").fill(url);
    await page.getByTestId("save-button").click();
    await page.waitForURL(/\/dashboard\/\d+/);
    return page.url().split("/").pop();
}

/**
 * Add a push monitor through the form
 * @param {string} name Monitor name
 * @returns {Promise<{id: string, pushURL: string}>} Monitor id and push URL
 */
async function addPushMonitor(name) {
    await page.goto("/add");
    await login();
    await page.getByTestId("monitor-type-select").selectOption("push");
    await page.getByTestId("friendly-name-input").fill(name);
    await page.getByTestId("save-button").click();
    await page.waitForURL(/\/dashboard\/\d+/);
    const href = await page.locator('a[href*="/api/push/"]').first().getAttribute("href");
    return { id: page.url().split("/").pop(), pushURL: href };
}

/**
 * Add a status page and open its editor
 * @param {string} name Page title
 * @param {string} slug Page slug
 * @returns {Promise<void>}
 */
async function newStatusPage(name, slug) {
    await page.goto("/add-status-page");
    await page.getByTestId("name-input").fill(name);
    await page.getByTestId("slug-input").fill(slug);
    await page.getByTestId("submit-button").click();
    await page.waitForURL(`**/status/${slug}?edit`);
    await waitForConfigLoaded();
}

/**
 * Save the page, which reloads it
 * @returns {Promise<void>}
 */
async function saveAndReload() {
    await Promise.all([page.waitForEvent("load"), page.getByTestId("save-button").click()]);
    await count(page.getByTestId("edit-sidebar"), 0);
}

// First run: the account, through the API, then the login form.
const setup = await page.request.post("/api/setup", { data: { username: "admin", password: PASSWORD } });
if (!setup.ok()) {
    throw new Error(`setup: ${setup.status()} ${await setup.text()}`);
}
await page.goto("/dashboard");
await login();

await run(
    "create and edit",
    async () => {
        const monitorName = "Monitor for Status Page";
        const tagValue = "Acme Inc";
        const tagValue2 = "Phoenix";
        const monitorCustomUrl = "https://www.example.com";
        await page.goto("/add");
        await page.getByTestId("monitor-type-select").selectOption("http");
        await page.getByTestId("friendly-name-input").fill(monitorName);
        await page.getByTestId("url-input").fill("https://www.example.com/status");
        await page.getByTestId("add-tag-button").click();
        await page.getByTestId("tag-name-input").fill("Client");
        await page.getByTestId("tag-value-input").fill(tagValue);
        await page.getByTestId("tag-color-select").click();
        await page.getByTestId("tag-color-select").getByRole("option", { name: "Orange" }).click();
        await page.getByRole("button", { name: "Add Another Tag" }).click();
        await page.getByTestId("tag-name-input").fill("Project");
        await page.getByTestId("tag-value-input").fill(tagValue2);
        await page.getByTestId("tag-color-select").click();
        await page.getByTestId("tag-color-select").getByRole("option", { name: "Blue" }).click();
        await page.getByTestId("add-tags-final-button").click();
        await page.getByTestId("save-button").click();
        await page.waitForURL(/\/dashboard\/\d+/);

        await newStatusPage("Example", "example");
        await page.getByTestId("description-input").fill("This is an example status page.");
        await page.getByTestId("footer-text-input").fill("This is footer text.");
        await page.getByTestId("refresh-interval-input").fill("30");
        await page.getByTestId("theme-select").selectOption("dark");
        await page.getByTestId("show-tags-checkbox").uncheck();
        await page.getByTestId("show-powered-by-checkbox").uncheck();
        await page.getByTestId("show-certificate-expiry-checkbox").uncheck();
        await page.getByTestId("analytics-type-select").selectOption("google");
        await page.getByTestId("analytics-id-input").fill("G-123");
        await page
            .getByTestId("custom-css-input")
            .getByTestId("textarea")
            .fill("body { background: rgb(0, 128, 128) !important; }");

        await page.getByTestId("create-incident-button").click();
        await page.getByTestId("incident-title").fill("Example Outage Incident");
        await page.getByTestId("incident-content-editable").fill("Sample incident message.");
        await page.getByTestId("post-incident-button").click();

        await page.getByTestId("add-group-button").click();
        await page.getByTestId("group-name").fill("Example Group 1");
        await page.getByTestId("monitor-select").click();
        await page.getByTestId("monitor-select").getByRole("option", { name: monitorName }).click();
        await count(page.getByTestId("monitor"), 1);
        await page.getByTestId("monitor-settings").click();
        await page.getByTestId("show-clickable-link").check();
        await page.getByTestId("custom-url-input").fill(monitorCustomUrl);
        await page.getByTestId("monitor-settings-close").click();
        await page.screenshot({ path: path.join(shots, "1-editor.png"), fullPage: true });

        await saveAndReload();
        await count(page.getByTestId("incident"), 1);
        await contains(page.getByTestId("incident-title"), "Example Outage Incident");
        await contains(page.getByTestId("incident-content"), "Sample incident message.");
        await contains(page.getByTestId("group-name"), "Example Group 1");
        await count(page.getByTestId("powered-by"), 0);
        await until(
            "monitor link",
            async () => (await page.getByTestId("monitor-name").getAttribute("href")) === monitorCustomUrl
        );
        await contains(page.getByTestId("update-countdown-text"), "00:");
        await until("dark theme", async () => (await page.locator("body").getAttribute("class"))?.includes("dark"));
        await until("google analytics in head", async () => (await page.locator("head").innerHTML()).includes("G-123"));
        const bg = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
        if (bg !== "rgb(0, 128, 128)") {
            throw new Error(`custom CSS: ${bg}`);
        }
        await page.screenshot({ path: path.join(shots, "2-saved.png"), fullPage: true });

        await openEditSidebar();
        await page.getByTestId("show-tags-checkbox").setChecked(true);
        await page.getByTestId("show-powered-by-checkbox").setChecked(true);
        await page.getByTestId("analytics-type-select").selectOption("umami");
        await page.getByTestId("analytics-script-url-input").fill("https://umami.example.com/script.js");
        await page.getByTestId("analytics-id-input").fill("606487e2-bc25-45f9-9132-fa8b065aad46");
        await saveAndReload();
        await contains(page.getByTestId("powered-by"), "Powered by");
        await visible(page.getByTestId("monitor-tag").filter({ hasText: tagValue }));
        await visible(page.getByTestId("monitor-tag").filter({ hasText: tagValue2 }));
        await until("umami in head", async () => {
            const head = await page.locator("head").innerHTML();
            return (
                head.includes("https://umami.example.com/script.js") &&
                head.includes("606487e2-bc25-45f9-9132-fa8b065aad46")
            );
        });

        await openEditSidebar();
        await page.getByTestId("analytics-type-select").selectOption("plausible");
        await page.getByTestId("analytics-script-url-input").fill("https://plausible.example.com/js/script.js");
        await page.getByTestId("analytics-id-input").fill("one.com,two.com");
        await saveAndReload();
        await until("plausible in head", async () => {
            const head = await page.locator("head").innerHTML();
            return head.includes("https://plausible.example.com/js/script.js") && head.includes("one.com,two.com");
        });

        await openEditSidebar();
        await page.getByTestId("analytics-type-select").selectOption("matomo");
        await page.getByTestId("analytics-script-url-input").fill("https://matomoto.example.com");
        await page.getByTestId("analytics-id-input").fill("123456789");
        await saveAndReload();
        // Upstream checks for the whole URL, which Node.js put after "//", making a broken
        // tracker address. This service uses only the host, so the check is for that.
        await until("matomo in head", async () => {
            const head = await page.locator("head").innerHTML();
            return head.includes(`"//" + "matomoto.example.com" + "/"`) && head.includes("123456789");
        });
        await page.screenshot({ path: path.join(shots, "3-tags-and-powered-by.png"), fullPage: true });
    },
    page
);

await run(
    "RSS feed escapes malicious monitor names",
    async () => {
        const names = [
            "<script>alert(1)</script>",
            "x</title><script>alert(document.domain)</script><title>",
            "Production API Server",
        ];
        const hosts = [
            "https://malicious1.example.com",
            "https://malicious2.example.com",
            "https://normal.example.com",
        ];
        for (let i = 0; i < 3; i++) {
            await addHttpMonitor(names[i], hosts[i]);
        }
        await newStatusPage("Security Test", "security-test");
        await page.getByTestId("add-group-button").click();
        await page.getByTestId("group-name").fill("Test Group");
        for (const name of names) {
            await page.getByTestId("monitor-select").click();
            await page.getByTestId("monitor-select").getByRole("option", { name }).click();
        }
        await page.getByTestId("save-button").click();
        await count(page.getByTestId("edit-sidebar"), 0);

        // The monitors' hosts don't resolve, so they go down after their first check.
        let rss = "";
        await until(
            "all down in RSS",
            async () => {
                const res = await page.request.get("/status/security-test/rss");
                if (res.status() !== 200 || res.headers()["content-type"] !== "application/rss+xml; charset=utf-8") {
                    throw new Error(`rss ${res.status()} ${res.headers()["content-type"]}`);
                }
                rss = await res.text();
                return names.every((n) => rss.includes(`<title><![CDATA[${n} is down]]></title>`));
            },
            90000
        );
        for (const want of ['<?xml version="1.0"', "<rss", "</rss>", "<title>Security Test RSS Feed</title>"]) {
            if (!rss.includes(want)) {
                throw new Error(`rss lacks ${want}: ${rss}`);
            }
        }
        if (!/<link>https?:\/\/[^<]+\/status\/security-test<\/link>/.test(rss)) {
            throw new Error(`rss link: ${rss}`);
        }
        fs.writeFileSync(path.join(shots, "rss.xml"), rss);

        await openEditSidebar();
        await page.getByTestId("rss-title-input").fill("Custom RSS Feed Title");
        await page.getByTestId("save-button").click();
        await count(page.getByTestId("edit-sidebar"), 0);
        await until("custom RSS title", async () =>
            (await (await page.request.get("/status/security-test/rss")).text()).includes(
                "<title>Custom RSS Feed Title</title>"
            )
        );
    },
    page
);

let publicMonitorID;
await run(
    "public page shows a published monitor to logged-out visitors",
    async () => {
        publicMonitorID = await addHttpMonitor("Public Monitor", "https://example.com");
        await newStatusPage("Public Demo", "public-demo");
        await page.getByTestId("add-group-button").click();
        await page.getByTestId("group-name").fill("Core");
        await page.getByTestId("monitor-select").click();
        await page.getByTestId("monitor-select").getByRole("option", { name: "Public Monitor" }).click();
        await count(page.getByTestId("monitor"), 1);
        await saveAndReload();

        const visitor = await browser.newContext({
            baseURL,
            viewport: { width: 1200, height: 900 },
            timezoneId: "UTC",
        });
        const anon = await visitor.newPage();
        await anon.goto("/status/public-demo");
        await contains(anon.getByTestId("group-name"), "Core");
        await contains(anon.getByTestId("monitor-name"), "Public Monitor");
        await count(anon.getByTestId("edit-button"), 0);
        // The page refreshes every 5 minutes, so reload until the monitor's first beat shows.
        await until(
            "uptime shown",
            async () => {
                await anon.reload();
                await contains(anon.getByTestId("monitor-name"), "Public Monitor");
                return (await anon.getByTestId("monitor").first().textContent()).includes("100%");
            },
            60000
        );
        // The overall status and the monitor's pill come from its newest beat.
        await contains(anon.locator(".overall-status"), "All Systems Operational");
        await until(
            "green pill",
            async () => (await anon.getByTestId("monitor").first().locator(".badge.bg-primary").count()) > 0
        );
        await anon.screenshot({ path: path.join(shots, "4-public.png"), fullPage: true });
        const badge = await anon.request.get(`/api/badge/${publicMonitorID}/avg-response/24`);
        if (badge.status() !== 200 || !(await badge.text()).includes("<svg")) {
            throw new Error(`badge: ${badge.status()}`);
        }
        await visitor.close();
    },
    page
);

await run(
    "public page shows an active maintenance window",
    async () => {
        const { pushURL } = await addPushMonitor("Maintenance Monitor");
        await newStatusPage("Maintenance Demo", "maintenance-demo");
        await page.getByTestId("add-group-button").click();
        await page.getByTestId("group-name").fill("Core");
        await page.getByTestId("monitor-select").click();
        await page.getByTestId("monitor-select").getByRole("option", { name: "Maintenance Monitor" }).click();
        await saveAndReload();

        await page.goto("/add-maintenance");
        await page.locator("#name").fill("Planned Window");
        await page.locator("#strategy").selectOption("single");
        await page.getByText("Pick Affected Monitors…").click();
        await page.getByRole("option", { name: "Maintenance Monitor" }).click();
        await page.keyboard.press("Escape");
        await page.getByText("Select status pages…").click();
        await page.getByRole("option", { name: "Maintenance Demo" }).click();
        await page.keyboard.press("Escape");
        await page.locator("#monitor-submit-btn").click();
        await page.waitForURL(/\/maintenance$/);
        await page.request.get(pushURL);

        const visitor = await browser.newContext({ baseURL, timezoneId: "UTC" });
        const anon = await visitor.newPage();
        await anon.goto("/status/maintenance-demo");
        await contains(anon.locator(".bg-maintenance"), "Planned Window");
        await contains(anon.getByTestId("monitor-name"), "Maintenance Monitor");
        await anon.screenshot({ path: path.join(shots, "5-maintenance.png"), fullPage: true });
        await visitor.close();
    },
    page
);

await run(
    "incident history: resolved incidents and paging",
    async () => {
        await newStatusPage("Empty Test", "empty-test");
        await page.getByTestId("save-button").click();
        await count(page.getByTestId("edit-sidebar"), 0);
        await count(page.locator(".past-incidents-section"), 0);

        await newStatusPage("Resolve Test", "resolve-test");
        await page.getByTestId("create-incident-button").click();
        await page.getByTestId("incident-title").fill("Resolved Incident");
        await page.getByTestId("incident-content-editable").fill("This incident will be resolved");
        await page.getByTestId("post-incident-button").click();
        const banner = page.getByTestId("incident").filter({ hasText: "Resolved Incident" });
        await visible(banner);
        await banner.locator("button", { hasText: "Resolve" }).click();
        await count(banner, 0);
        await page.getByTestId("save-button").click();
        await count(page.getByTestId("edit-sidebar"), 0);
        await page.goto("/status/resolve-test");
        await contains(page.locator(".past-incidents-section .incident-title"), "Resolved Incident");

        await newStatusPage("Pagination Test", "pagination-test");
        for (let i = 1; i <= 12; i++) {
            await page.getByTestId("create-incident-button").click();
            await page.getByTestId("incident-title").fill("Incident " + i);
            await page.getByTestId("incident-content-editable").fill("Content for incident " + i);
            await page.getByTestId("post-incident-button").click();
            await sleep(300);
            const resolve = page.locator("button", { hasText: "Resolve" }).first();
            if (await resolve.isVisible()) {
                await resolve.click();
                await sleep(300);
            }
        }
        await page.getByTestId("save-button").click();
        await count(page.getByTestId("edit-sidebar"), 0);
        await visible(page.locator(".past-incidents-section"));
        await until(
            "ten incidents first",
            async () => (await page.locator(".past-incidents-section .incident-title").count()) === 10
        );
        await page.locator("button", { hasText: "Load More" }).click();
        await until(
            "all twelve",
            async () => (await page.locator(".past-incidents-section .incident-title").count()) === 12
        );
        await page.screenshot({ path: path.join(shots, "6-incident-history.png"), fullPage: true });
    },
    page
);

await run(
    "badges for a push monitor",
    async () => {
        const { id, pushURL } = await addPushMonitor("Badge Monitor");
        await page.request.get(`${pushURL}?ping=50`);
        for (const kind of ["uptime/24", "ping/24"]) {
            const res = await page.request.get(`/api/badge/${id}/${kind}`);
            if (res.status() !== 200 || !(await res.text()).includes("<svg")) {
                throw new Error(`${kind}: ${res.status()}`);
            }
        }
    },
    page
);

await browser.close();
fs.writeFileSync(path.join(shots, "results.json"), JSON.stringify({ results, consoleErrors }, null, 2));
console.log(`${results.filter((r) => r.ok).length}/${results.length} passed; page errors: ${consoleErrors.length}`);
for (const e of consoleErrors.slice(0, 10)) {
    console.log("page error:", e);
}
process.exit(results.every((r) => r.ok) ? 0 : 1);
