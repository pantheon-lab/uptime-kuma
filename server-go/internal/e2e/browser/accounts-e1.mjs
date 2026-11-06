// E1 against the built image: Settings > Accounts lists every account with its role, how it logs
// in, its last login and open sessions; an admin changes a password account's role, ends an
// account's sessions and removes an account, and can't remove or demote themselves.
//
// Usage: node accounts-e1.mjs <playwright-core dir> <chromium> <base URL> <screenshot dir> <psql command>
//
// It needs a new database. The psql command runs SQL in it from standard input, and makes a
// password viewer and a GitHub account, as a GitHub login would.
import { execSync } from "node:child_process";
import { createRequire } from "node:module";
import path from "node:path";

const [pwDir, chromiumPath, baseURL, shots, psqlCommand] = process.argv.slice(2);
const require = createRequire(path.join(pwDir, "index.js"));
const { chromium } = require(pwDir);
const password = "Kuma-E2E-E1-w4Hq9Zt2";

const browser = await chromium.launch({ executablePath: chromiumPath });
const options = { baseURL, viewport: { width: 1400, height: 1000 }, timezoneId: "UTC" };
const adminContext = await browser.newContext(options);
const viewerContext = await browser.newContext(options);
const page = await adminContext.newPage();
const viewerPage = await viewerContext.newPage();
const errors = [];
page.on("pageerror", (e) => errors.push(e.message));
viewerPage.on("pageerror", (e) => errors.push(e.message));
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
 * @param {object} p Page
 * @param {string} username Account
 * @returns {Promise<void>}
 */
async function login(p, username) {
    await p.goto("/dashboard");
    await p.getByPlaceholder("Username").fill(username);
    await p.getByPlaceholder("Password").fill(password);
    await p.getByRole("button", { name: "Log in" }).click();
    await p.waitForSelector("text=Add New Monitor, text=No Monitors", { timeout: 10000 }).catch(() => {});
    await p.waitForFunction(
        () => document.querySelector("#app").__vue_app__._container._vnode.component.proxy.loggedIn
    );
}

/**
 * The root component's state
 * @param {object} p Page
 * @param {string} expression Read from the root, such as "role"
 * @returns {Promise<any>} Value
 */
const root = (p, expression) =>
    p.evaluate((e) => document.querySelector("#app").__vue_app__._container._vnode.component.proxy[e], expression);

/**
 * Send a realtime event from a page, and return its answer
 * @param {object} p Page
 * @param {Array} args Event and arguments
 * @returns {Promise<object>} Answer
 */
const emit = (p, ...args) =>
    p.evaluate(
        (a) =>
            new Promise((resolve) =>
                document
                    .querySelector("#app")
                    .__vue_app__._container._vnode.component.proxy.getSocket()
                    .emit(...a, resolve)
            ),
        args
    );

const account = (username) => page.getByTestId(`account-${username}`);

await step("set up an admin, a password viewer and a GitHub account", async () => {
    const setup = await page.request.post("/api/setup", { data: { username: "admin", password } });
    if ((await setup.json()).ok !== true) {
        throw new Error("setup failed");
    }
    execSync(psqlCommand, {
        input: `INSERT INTO app_user (username, password_hash, role) SELECT 'viewer', password_hash, 'viewer' FROM app_user WHERE username = 'admin';
                INSERT INTO app_user (username, password_hash, role, github_id, github_token, github_checked_at)
                VALUES ('@octocat', '', 'admin', 583231, 'gho_fake', now());`,
    });
    await login(page, "admin");
    await login(viewerPage, "viewer");
    if ((await root(viewerPage, "role")) !== "viewer") {
        throw new Error("the viewer isn't a viewer");
    }
});

await step("the page lists every account", async () => {
    await page.locator(".dropdown-profile-pic, .profile-pic").first().click();
    await page.getByRole("link", { name: "Settings" }).click();
    await page.getByRole("link", { name: "Accounts" }).click();
    await page.waitForURL(/\/settings\/accounts$/);
    await account("@octocat").waitFor();
    const text = {};
    for (const name of ["admin", "viewer", "@octocat"]) {
        text[name] = (await account(name).innerText()).replace(/\s+/g, " ");
    }
    const want = [
        ["admin", /You/],
        ["admin", /Admin · Logs in with a password/],
        ["admin", /Open sessions: 1/],
        ["admin", /This session/],
        ["viewer", /Viewer · Logs in with a password/],
        ["viewer", /Open sessions: 1/],
        ["@octocat", /Admin · Logs in with GitHub/],
        ["@octocat", /GitHub teams last checked: \d{4}-/],
        ["@octocat", /Last login: Never/],
    ];
    for (const [name, re] of want) {
        if (!re.test(text[name])) {
            throw new Error(`${name}: ${text[name]} doesn't match ${re}`);
        }
    }
    if (
        !(await account("admin").getByTestId("remove").isDisabled()) ||
        !(await account("admin").getByTestId("role").isDisabled())
    ) {
        throw new Error("the admin can remove or demote themselves");
    }
    if ((await account("@octocat").getByTestId("role").count()) !== 0) {
        throw new Error("a GitHub account's role can be changed");
    }
    await page.screenshot({ path: path.join(shots, "accounts.png"), fullPage: true });
});

await step("making the viewer an admin takes effect on their open page", async () => {
    await account("viewer").getByTestId("role").selectOption("admin");
    await page.waitForSelector("text=Saved.");
    await viewerPage.waitForFunction(
        () => document.querySelector("#app").__vue_app__._container._vnode.component.proxy.role === "admin",
        null,
        { timeout: 15000 }
    );
    await account("viewer").getByText("Admin · Logs in with a password").waitFor();
});

await step("ending the account's sessions logs them out", async () => {
    await account("viewer").getByTestId("end-sessions").click();
    await page.getByText("Log viewer out everywhere?").waitFor();
    await page.locator(".modal.show").getByRole("button", { name: "Yes" }).click();
    await account("viewer").getByText("Open sessions: 0").waitFor();
    await viewerPage.reload();
    await viewerPage.getByPlaceholder("Username").waitFor({ timeout: 10000 });
});

await step("removing a GitHub account warns that it can come back", async () => {
    await account("@octocat").getByTestId("remove").click();
    const dialog = page.locator(".modal.show");
    await dialog.getByText("still in the GitHub teams").waitFor();
    await page.screenshot({ path: path.join(shots, "remove-github.png") });
    await dialog.getByRole("button", { name: "Yes" }).click();
    await account("@octocat").waitFor({ state: "detached" });
});

await step("the server refuses removing or demoting yourself, and a viewer's requests", async () => {
    const id = (await emit(page, "getAccountList")).accounts.find((a) => a.you).id;
    const removed = await emit(page, "deleteAccount", id);
    const demoted = await emit(page, "setAccountRole", id, "viewer");
    if (removed.ok !== false || demoted.ok !== false || !/your own account/.test(removed.msg)) {
        throw new Error(JSON.stringify([removed, demoted]));
    }
    // The other admin is made a viewer again, and asks for the list.
    const viewerID = (await emit(page, "getAccountList")).accounts.find((a) => a.username === "viewer").id;
    await emit(page, "setAccountRole", viewerID, "viewer");
    await login(viewerPage, "viewer");
    const list = await emit(viewerPage, "getAccountList");
    if (list.ok !== false || !/can only view/.test(list.msg)) {
        throw new Error(JSON.stringify(list));
    }
    return removed.msg;
});

await browser.close();
const passed = results.filter(Boolean).length;
console.log(`${passed}/${results.length} passed; page errors: ${errors.length}`);
for (const e of errors.slice(0, 5)) {
    console.log("page error:", e);
}
process.exit(passed === results.length ? 0 : 1);
