// Settings > Single Sign-On against the built image, with a fake GitHub: setting up GitHub login in
// the UI, logging in with it at once, turning passwords off only from a GitHub login, GitHub's
// settings held while passwords are off, and viewers kept out.
//
// Usage: node sso.mjs <playwright-core dir> <chromium> <base URL> <screenshot dir>
//
// It needs a new database, with no KUMA_GITHUB_* or KUMA_PASSWORD_LOGIN variables. The service
// must reach this machine's 127.0.0.1, where the script runs the fake GitHub.
import http from "node:http";
import { createRequire } from "node:module";
import path from "node:path";

const [pwDir, chromiumPath, baseURL, shots] = process.argv.slice(2);
const require = createRequire(path.join(pwDir, "index.js"));
const { chromium } = require(pwDir);
const password = "Kuma-E2E-SSO-v6Hn3Qp8";

// A fake GitHub: authorize sends the browser straight back as nextUser, who has these teams.
const people = {
    alice: { id: 101, teams: ["sre"] },
    bob: { id: 102, teams: ["support"] },
};
let nextUser = "alice";
const github = http.createServer((req, res) => {
    const u = new URL(req.url, "http://x");
    const json = (status, body) => {
        res.writeHead(status, { "Content-Type": "application/json" });
        res.end(JSON.stringify(body));
    };
    if (u.pathname === "/login/oauth/authorize") {
        const back = new URL("/api/auth/github/callback", baseURL);
        back.searchParams.set("code", "code-" + nextUser);
        back.searchParams.set("state", u.searchParams.get("state"));
        res.writeHead(302, { Location: back.href });
        res.end();
        return;
    }
    if (u.pathname === "/login/oauth/access_token") {
        let body = "";
        req.on("data", (c) => (body += c));
        req.on("end", () => {
            const form = new URLSearchParams(body);
            if (form.get("client_id") !== "kuma-client" || form.get("client_secret") !== "kuma-secret") {
                json(200, { error: "incorrect_client_credentials" });
                return;
            }
            json(200, { access_token: "t-" + form.get("code").slice(5), token_type: "bearer", scope: "read:org" });
        });
        return;
    }
    const login = (req.headers.authorization || "").replace("Bearer t-", "");
    const person = people[login];
    if (!person) {
        json(401, { message: "Bad credentials" });
    } else if (u.pathname === "/user") {
        json(200, { id: person.id, login });
    } else if (u.pathname === "/user/memberships/orgs/acme") {
        json(200, { state: "active" });
    } else if (u.pathname === "/user/teams") {
        json(
            200,
            person.teams.map((slug) => ({ slug, organization: { login: "acme" } }))
        );
    } else {
        json(404, {});
    }
});
await new Promise((resolve) => github.listen(0, "127.0.0.1", resolve));
const githubURL = `http://127.0.0.1:${github.address().port}`;

const browser = await chromium.launch({ executablePath: chromiumPath });
const errors = [];
const results = [];
let current;

/**
 * A new browser profile
 * @param {string} who Whose
 * @returns {Promise<object>} Page
 */
async function profile(who) {
    const context = await browser.newContext({ baseURL, viewport: { width: 1300, height: 900 }, timezoneId: "UTC" });
    const page = await context.newPage();
    page.on("pageerror", (e) => errors.push(`${who}: ${e.message}`));
    page.on(
        "console",
        (m) => m.type() === "error" && /TypeError|ReferenceError/.test(m.text()) && errors.push(`${who}: ${m.text()}`)
    );
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
 * Log in with GitHub as someone, from the login page
 * @param {string} who A fake GitHub user
 * @returns {Promise<object>} Their page, on the dashboard
 */
async function githubLogin(who) {
    const page = await profile(who);
    current = page;
    nextUser = who;
    await page.goto("/dashboard");
    await page.locator("#github-login").click();
    await page.waitForURL(/\/dashboard/, { timeout: 15000 });
    await page.getByText("Quick Stats").waitFor({ timeout: 20000 });
    return page;
}

let admin;
let alice;

await step("an admin with a password sees GitHub off, and can't turn passwords off", async () => {
    const setup = await fetch(new URL("/api/setup", baseURL), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ username: "admin", password }),
    });
    expect((await setup.json()).ok === true, "setup failed");
    admin = await profile("admin");
    current = admin;
    await admin.goto("/dashboard");
    await admin.getByPlaceholder("Username").fill("admin");
    await admin.getByPlaceholder("Password").fill(password);
    await admin.getByRole("button", { name: "Log in" }).click();
    await admin.getByText("Quick Stats").waitFor({ timeout: 20000 });
    await admin.goto("/settings/sso");
    await admin.getByTestId("sso-settings").getByText("Allow logging in with GitHub").waitFor();
    expect(!(await admin.getByTestId("sso-github-enabled").isChecked()), "GitHub is on");
    expect(await admin.getByTestId("sso-password-login").isDisabled(), "passwords can be turned off");
    const help = await admin.getByTestId("sso-password-help").innerText();
    expect(help.includes("log in with GitHub as an admin"), help);
    expect((await admin.locator(".settings-menu").getByText("Single Sign-On").count()) === 1, "not in the menu");
});

await step("setting up GitHub in the form turns its login on at once", async () => {
    await admin.getByTestId("sso-github-enabled").check();
    const callback = await admin.getByTestId("sso-callback").locator("input").inputValue();
    expect(callback === `${baseURL}/api/auth/github/callback`, `callback: ${callback}`);
    await admin.getByTestId("sso-client-id").fill("kuma-client");
    // The label names the field
    await admin.getByLabel("Client secret").fill("kuma-secret");
    await admin.getByTestId("sso-org").fill("acme");
    await admin.getByTestId("sso-admin-teams").fill("SRE");
    await admin.getByTestId("sso-viewer-teams").fill("support");
    await admin.getByText("GitHub Enterprise Server").click();
    await admin.locator("#sso-web-url").fill(githubURL);
    await admin.locator("#sso-api-url").fill(githubURL);
    await admin.screenshot({ path: path.join(shots, "sso-form.png"), fullPage: true });
    await admin.getByTestId("sso-save").click();
    await admin.getByText("Saved.").first().waitFor();
    const placeholder = await admin.getByLabel("Client secret").getAttribute("placeholder");
    expect(placeholder === "Saved. Leave blank to keep it.", `secret placeholder: ${placeholder}`);
    expect((await admin.getByTestId("sso-admin-teams").inputValue()) === "sre", "teams not lowercased");
    const login = await profile("guest");
    await login.goto("/dashboard");
    await login.locator("#github-login").waitFor({ timeout: 15000 });
    await login.context().close();
});

await step("an admin team's member logs in with GitHub", async () => {
    alice = await githubLogin("alice");
    await alice.goto("/settings/sso");
    await alice.getByTestId("sso-last-login").getByText("@alice").waitFor();
    return await alice.getByTestId("sso-last-login").innerText();
});

await step("logged in with GitHub, an admin turns passwords off, and password login stops", async () => {
    current = alice;
    const toggle = alice.getByTestId("sso-password-login");
    expect(await toggle.isEnabled(), "passwords can't be turned off from a GitHub login");
    // Changing GitHub's settings at the same time isn't allowed
    await alice.getByTestId("sso-org").fill("acme-new");
    expect(await toggle.isDisabled(), "passwords can be turned off along with a new org");
    await alice.getByTestId("sso-org").fill("acme");
    await toggle.uncheck();
    await alice.getByTestId("sso-save").click();
    await alice.getByText("Saved.").first().waitFor();
    const guest = await profile("guest");
    await guest.goto("/dashboard");
    await guest.locator("#github-login").waitFor({ timeout: 15000 });
    expect((await guest.getByPlaceholder("Password").count()) === 0, "the password form is still there");
    const signIn = await fetch(new URL("/api/auth/sign-in/username", baseURL), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ username: "admin", password }),
    });
    expect(signIn.status === 403, `password sign-in: ${signIn.status}`);
    await guest.context().close();
});

await step("while passwords are off, GitHub's settings are held", async () => {
    await alice.getByTestId("sso-org").fill("elsewhere");
    await alice.getByTestId("sso-save").click();
    await alice.getByText("Password login can only be off with GitHub settings").waitFor();
    await alice.reload();
    await alice.getByTestId("sso-org").waitFor();
    expect((await alice.getByTestId("sso-org").inputValue()) === "acme", "the org changed");
    // Passwords on again, which a GitHub admin may do
    await alice.getByTestId("sso-password-login").check();
    await alice.getByTestId("sso-save").click();
    await alice.getByText("Saved.").first().waitFor();
});

await step("a viewer team's member is a viewer, without the page", async () => {
    const bob = await githubLogin("bob");
    await bob.getByTestId("view-only-badge").waitFor();
    await bob.goto("/settings/sso");
    await bob.waitForURL(/\/settings\/security$/, { timeout: 10000 });
    const answer = await bob.evaluate(
        () =>
            new Promise((resolve) =>
                document
                    .querySelector("#app")
                    .__vue_app__._container._vnode.component.proxy.getSocket()
                    .emit("getLoginSettings", resolve)
            )
    );
    expect(answer.ok === false, JSON.stringify(answer));
});

await step("turning GitHub off ends its login, keeping its settings", async () => {
    current = admin;
    await admin.goto("/settings/sso");
    await admin.getByTestId("sso-github-enabled").uncheck();
    await admin.getByTestId("sso-save").click();
    await admin.getByText("Saved.").first().waitFor();
    const res = await fetch(new URL("/api/auth/github/start", baseURL), { redirect: "manual" });
    expect(res.status === 404, `start: ${res.status}`);
    await admin.getByTestId("sso-github-enabled").check();
    expect((await admin.getByTestId("sso-client-id").inputValue()) === "kuma-client", "client ID lost");
    // Alice's session ends at her next request
    await alice.reload();
    await alice.getByPlaceholder("Password").waitFor({ timeout: 15000 });
});

await browser.close();
github.close();
const passed = results.filter(Boolean).length;
console.log(`${passed}/${results.length} passed; page errors: ${errors.length}`);
for (const e of errors.slice(0, 5)) {
    console.log("page error:", e);
}
process.exit(passed === results.length && errors.length === 0 ? 0 : 1);
