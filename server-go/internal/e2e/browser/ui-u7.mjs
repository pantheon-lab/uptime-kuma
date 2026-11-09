// U7 against the built image: the first run's guide, a viewer's pages, and phones.
//
// Usage: node ui-u7.mjs <playwright-core dir> <chromium> <base URL> <screenshot dir> <psql command>
//
// It needs a new database. The psql command runs SQL in the service's database, to add a viewer,
// such as "docker exec -i db psql -U postgres -d kuma -q".
import { execSync } from "node:child_process";
import { createRequire } from "node:module";
import path from "node:path";

const [pwDir, chromiumPath, baseURL, shots, psqlCommand] = process.argv.slice(2);
const require = createRequire(path.join(pwDir, "index.js"));
const { chromium } = require(pwDir);
const password = "Kuma-E2E-U7-h3Wn6Rc9";
const health = new URL("/healthz", baseURL).href;

const browser = await chromium.launch({ executablePath: chromiumPath });
const errors = [];
const results = [];
let current;

/**
 * A new browser profile, logged in
 * @param {string} user Account
 * @param {object} options Context options
 * @returns {Promise<object>} Page
 */
async function login(user, options = {}) {
    const context = await browser.newContext({ baseURL, viewport: { width: 1300, height: 900 }, ...options });
    const page = await context.newPage();
    page.on("pageerror", (e) => errors.push(`${user}: ${e.message}`));
    page.on(
        "console",
        (m) => m.type() === "error" && /TypeError|ReferenceError/.test(m.text()) && errors.push(`${user}: ${m.text()}`)
    );
    await page.goto("/dashboard");
    await page.getByPlaceholder("Username").fill(user);
    await page.getByPlaceholder("Password").fill(password);
    await page.getByRole("button", { name: "Log in" }).click();
    await page.getByText("Quick Stats").waitFor({ timeout: 20000 });
    return page;
}

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
        await current
            ?.screenshot({ path: path.join(shots, `fail-${name.replace(/\W+/g, "-")}.png`), fullPage: true })
            .catch(() => {});
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
 * Send a realtime event from a page, and return its answer
 * @param {object} page Page
 * @param {...any} args Event and arguments
 * @returns {Promise<object>} Answer
 */
const emit = (page, ...args) =>
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
 * Wait until the guide says how many steps are done
 * @param {object} page Page
 * @param {string} text Such as "1 of 3 done"
 * @returns {Promise<void>}
 */
const progress = (page, text) =>
    page.waitForFunction(
        (t) => document.querySelector('[data-testid="getting-started-progress"]')?.textContent.trim() === t,
        text,
        { timeout: 10000 }
    );

let admin;
let monitorID;

await step("a new server's dashboard has the guide, at its first step", async () => {
    const setup = await fetch(new URL("/api/setup", baseURL), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ username: "admin", password }),
    });
    expect((await setup.json()).ok === true, "setup failed");
    execSync(psqlCommand, {
        input: "INSERT INTO app_user (username, password_hash, role) SELECT 'viewer', password_hash, 'viewer' FROM app_user WHERE username = 'admin';",
    });
    admin = await login("admin");
    current = admin;
    await admin.getByTestId("getting-started").waitFor({ timeout: 10000 });
    await progress(admin, "0 of 3 done");
    const next = admin.locator(".guide-step.next");
    expect((await next.getAttribute("data-testid")) === "getting-started-monitor", "the first step isn't next");
    expect(
        (await next.locator(".btn-primary").innerText()).trim() === "Add New Monitor",
        "the next step's button isn't the main one"
    );
    const empty = (await admin.getByTestId("monitor-list").innerText()).trim();
    expect(empty.includes("No Monitors, please add one"), `the list says "${empty}"`);
});

await step("a viewer of a server with no monitors is told an admin adds them, and has no guide", async () => {
    const viewer = await login("viewer");
    current = viewer;
    await viewer.getByTestId("viewer-no-monitors").waitFor({ timeout: 10000 });
    expect((await viewer.getByTestId("getting-started").count()) === 0, "a viewer has the guide");
    const list = (await viewer.getByTestId("monitor-list").innerText()).trim();
    expect(list === "No monitors yet.", `the list says "${list}"`);
    await viewer.context().close();
});

await step("each step is ticked as it's done", async () => {
    current = admin;
    const m = await emit(admin, "add", {
        type: "http",
        name: "site",
        url: health,
        interval: 60,
        notificationIDList: {},
        accepted_statuscodes: ["200-299"],
    });
    expect(m.ok, JSON.stringify(m));
    monitorID = m.monitorID;
    await progress(admin, "1 of 3 done");
    // After the buttons' colour transition
    await admin.waitForTimeout(500);
    await admin.getByTestId("getting-started").screenshot({ path: path.join(shots, "u7-guide.png") });
    expect(
        (await admin.getByTestId("getting-started-monitor").getAttribute("data-done")) === "true",
        "the monitor step isn't done"
    );
    expect(
        (await admin.locator(".guide-step.next").getAttribute("data-testid")) === "getting-started-notification",
        "notifications aren't next"
    );
    const n = await emit(
        admin,
        "addNotification",
        { name: "hook", type: "webhook", webhookURL: "http://127.0.0.1:1/hook", isDefault: false },
        null
    );
    expect(n.ok, JSON.stringify(n));
    await progress(admin, "2 of 3 done");
});

await step("hiding the guide lasts", async () => {
    await admin.getByTestId("getting-started-hide").click();
    expect((await admin.getByTestId("getting-started").count()) === 0, "still shown");
    await admin.reload();
    await admin.getByText("Quick Stats").waitFor();
    await admin.waitForTimeout(1500);
    expect((await admin.getByTestId("getting-started").count()) === 0, "back after a reload");
});

await step("the last step finishes the guide for good", async () => {
    await admin.evaluate(() => localStorage.setItem("gettingStarted", "active"));
    await admin.reload();
    await progress(admin, "2 of 3 done");
    await admin.getByTestId("getting-started-statusPage").getByRole("link", { name: "Add New Status Page" }).click();
    await admin.waitForURL(/\/add-status-page/);
    const r = await emit(admin, "addStatusPage", "Public", "public");
    expect(r.ok, JSON.stringify(r));
    await admin.goto("/dashboard");
    await admin.getByText("Quick Stats").waitFor();
    await admin.waitForTimeout(1500);
    expect((await admin.getByTestId("getting-started").count()) === 0, "shown with every step done");
    const saved = await admin.evaluate(() => localStorage.getItem("gettingStarted"));
    expect(saved === "done", `saved "${saved}"`);
});

await step("a browser that first sees a server with monitors gets no guide", async () => {
    const other = await login("admin");
    current = other;
    await other.waitForTimeout(1500);
    expect((await other.getByTestId("getting-started").count()) === 0, "the guide started");
    await other.context().close();
});

let viewer;

await step("a viewer sees that the account can only view", async () => {
    viewer = await login("viewer");
    current = viewer;
    const badge = viewer.getByTestId("view-only-badge");
    await badge.waitFor({ timeout: 10000 });
    expect((await badge.innerText()).trim() === "View only", await badge.innerText());
    expect(
        (await badge.getAttribute("title")) === "Your account can only view. Ask an admin to make changes.",
        "no explanation"
    );
    expect((await admin.getByTestId("view-only-badge").count()) === 0, "an admin has the badge");
});

await step("a viewer sent from a page that edits goes somewhere near, and is told why", async () => {
    const cases = [
        [`/edit/${monitorID}`, `/dashboard/${monitorID}`],
        ["/settings/general", "/settings/security"],
        ["/maintenance/edit/1", "/maintenance"],
        ["/add-status-page", "/manage-status-page"],
        ["/add", "/dashboard"],
    ];
    const seen = [];
    for (const [from, to] of cases) {
        await viewer.goto(from);
        await viewer.waitForURL((u) => u.pathname === to, { timeout: 10000 });
        seen.push(`${from} → ${to}`);
    }
    // The pop-up, from the last of them
    await viewer.getByText("Your account can only view, so that page isn't open to it.").first().waitFor();
    await viewer.goto("/settings/notifications");
    await viewer.waitForURL((u) => u.pathname === "/settings/security");
    await viewer.getByText("Update Password").waitFor();
    return seen.join(", ");
});

await step("a viewer's status page has no editing", async () => {
    await viewer.goto("/status/public");
    await viewer.getByText("Go to Dashboard").waitFor({ timeout: 15000 });
    expect((await viewer.getByTestId("edit-button").count()) === 0, "Edit Status Page is shown");
    await viewer.goto("/status/public?edit");
    await viewer.getByText("Go to Dashboard").waitFor({ timeout: 15000 });
    await viewer.waitForTimeout(1000);
    expect((await viewer.getByTestId("edit-sidebar").count()) === 0, "the editor opened");
    // An admin still edits
    await admin.goto("/status/public");
    await admin.getByTestId("edit-button").click();
    await admin.getByTestId("edit-sidebar").waitFor({ timeout: 10000 });
});

await step("a viewer's pages have no buttons that change things", async () => {
    const changing = ["Edit", "Delete", "Pause", "Resume", "Clone", "Schedule Maintenance", "Add New Monitor"];
    const found = [];
    for (const page of ["/dashboard", `/dashboard/${monitorID}`, "/manage-status-page", "/maintenance"]) {
        await viewer.goto(page);
        await viewer.waitForTimeout(1500);
        const texts = await viewer.evaluate(() =>
            [...document.querySelectorAll("button, a.btn")]
                .filter((el) => el.offsetParent !== null && !el.closest(".modal"))
                .map((el) => el.innerText.replace(/\s+/g, " ").trim())
        );
        for (const t of texts) {
            if (changing.includes(t)) {
                found.push(`${page}: ${t}`);
            }
        }
    }
    expect(found.length === 0, found.join(", "));
});

const phone = { viewport: { width: 375, height: 760 }, isMobile: true, hasTouch: true };

await step("on a phone, the counts take two short rows", async () => {
    const page = await login("admin", phone);
    current = page;
    const box = await page.getByTestId("quick-stats").boundingBox();
    expect(box.height < 200, `the counts are ${box.height}px high`);
    const tops = await page
        .locator('[data-testid^="quick-stat-"]')
        .evaluateAll((els) => [...new Set(els.map((e) => Math.round(e.getBoundingClientRect().top)))]);
    expect(tops.length === 2, `${tops.length} rows`);
    const scroll = await page.evaluate(() => document.documentElement.scrollWidth);
    expect(scroll <= 375, `the page is ${scroll}px wide`);
    await page.screenshot({ path: path.join(shots, "u7-phone-dashboard.png") });
    await page.context().close();
    return `${Math.round(box.height)}px`;
});

await step("on a phone, Save stays above the navigation, and Test's result has a line of its own", async () => {
    const page = await login("admin", phone);
    current = page;
    await page.goto("/add");
    await page.getByTestId("url-input").fill(health);
    await page.evaluate(() => window.scrollTo(0, 600));
    await page.waitForTimeout(400);
    const at = await page.evaluate(() => {
        const bar = document.querySelector(".fixed-bottom-bar").getBoundingClientRect();
        const nav = document.querySelector(".bottom-nav").getBoundingClientRect();
        return { bar: bar.bottom, nav: nav.top };
    });
    expect(at.bar <= at.nav + 1, `the bar ends at ${at.bar}, the navigation starts at ${at.nav}`);
    await page.getByTestId("save-button").click({ trial: true });
    await page.getByTestId("test-button").click();
    await page.getByTestId("test-result").waitFor({ timeout: 20000 });
    const save = await page.getByTestId("save-button").boundingBox();
    const result = await page.getByTestId("test-result").boundingBox();
    expect(result.y >= save.y + save.height - 1, "the result shares the buttons' line");
    await page.screenshot({ path: path.join(shots, "u7-phone-form.png") });
    await page.context().close();
});

await step("on a phone, a viewer's note sits above the navigation", async () => {
    const page = await login("viewer", phone);
    current = page;
    const note = page.getByTestId("view-only-badge");
    await note.waitFor();
    const box = await note.boundingBox();
    const nav = await page.locator(".bottom-nav").boundingBox();
    expect(Math.abs(box.y + box.height - nav.y) <= 1, `note ends at ${box.y + box.height}, navigation at ${nav.y}`);
    await page.context().close();
});

await browser.close();
const passed = results.filter(Boolean).length;
console.log(`${passed}/${results.length} passed; page errors: ${errors.length}`);
for (const e of errors.slice(0, 5)) {
    console.log("page error:", e);
}
process.exit(passed === results.length && errors.length === 0 ? 0 : 1);
