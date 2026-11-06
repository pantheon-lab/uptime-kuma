// E2 against the built image: the Security page of an account that logs in with GitHub shows its
// organization, teams, role and last check instead of the password and two-factor settings, and
// the server refuses those changes; a password account's page is unchanged.
//
// Usage: node security-e2.mjs <playwright-core dir> <chromium> <base URL> <screenshot dir> <fake GitHub port>
//
// Start the image as for github-7b.mjs (KUMA_GITHUB_ADMIN_TEAMS=sre), on a new database.
import { createHash, randomBytes } from "node:crypto";
import { createServer } from "node:http";
import { createRequire } from "node:module";
import path from "node:path";

const [pwDir, chromiumPath, baseURL, shots, githubPort] = process.argv.slice(2);
const require = createRequire(path.join(pwDir, "index.js"));
const { chromium } = require(pwDir);
const password = "Kuma-E2E-E2-r7Tm4Kd9";

const people = {
    alice: { id: 21, member: true, teams: ["sre", "platform"] },
};
let current = "alice";
const codes = new Map();
const tokens = new Map();
const github = createServer((req, res) => {
    const url = new URL(req.url, `http://127.0.0.1:${githubPort}`);
    const json = (status, body) => {
        res.writeHead(status, { "Content-Type": "application/json" });
        res.end(JSON.stringify(body));
    };
    if (url.pathname === "/login/oauth/authorize") {
        const q = url.searchParams;
        if (
            q.get("client_id") !== "client" ||
            q.get("scope") !== "read:org" ||
            q.get("code_challenge_method") !== "S256"
        ) {
            return json(400, { error: "bad request" });
        }
        const code = randomBytes(8).toString("hex");
        codes.set(code, { who: current, challenge: q.get("code_challenge") });
        res.writeHead(302, { Location: `${baseURL}/api/auth/github/callback?code=${code}&state=${q.get("state")}` });
        return res.end();
    }
    if (url.pathname === "/login/oauth/access_token") {
        let body = "";
        req.on("data", (c) => (body += c));
        req.on("end", () => {
            const form = new URLSearchParams(body);
            const grant = codes.get(form.get("code"));
            codes.delete(form.get("code"));
            const challenge = createHash("sha256")
                .update(form.get("code_verifier") ?? "")
                .digest("base64url");
            if (!grant || grant.challenge !== challenge || form.get("client_secret") !== "secret") {
                return json(200, { error: "bad_verification_code" });
            }
            const token = "gho_" + randomBytes(8).toString("hex");
            tokens.set(token, grant.who);
            json(200, { access_token: token, token_type: "bearer", scope: "read:org" });
        });
        return;
    }
    const who = tokens.get((req.headers.authorization ?? "").replace("Bearer ", ""));
    const person = people[who];
    if (!person) {
        return json(401, { message: "Bad credentials" });
    }
    if (url.pathname === "/user") {
        return json(200, { id: person.id, login: who });
    }
    if (url.pathname === "/user/memberships/orgs/pantheon-lab") {
        return person.member ? json(200, { state: "active" }) : json(404, {});
    }
    if (url.pathname === "/user/teams") {
        return json(
            200,
            person.teams.map((slug) => ({ slug, organization: { login: "pantheon-lab" } }))
        );
    }
    json(404, {});
});
await new Promise((resolve) => github.listen(Number(githubPort), "127.0.0.1", resolve));

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

await step("set up a password admin, and log in with GitHub", async () => {
    const setup = await page.request.post("/api/setup", { data: { username: "admin", password } });
    if ((await setup.json()).ok !== true) {
        throw new Error("setup failed");
    }
    current = "alice";
    await page.goto("/dashboard");
    await page.locator("#github-login").click();
    await page.waitForSelector("text=Add New Monitor");
});

await step("the Security page shows the GitHub account", async () => {
    await page.goto("/settings/security");
    const box = page.getByTestId("github-account");
    await box.waitFor();
    const text = (await box.innerText()).replace(/\s+/g, " ");
    for (const want of [
        /Organization pantheon-lab/,
        /Teams platform ?sre/,
        /Role Admin/,
        /GitHub teams last checked \d{4}-/,
    ]) {
        if (!want.test(text)) {
            throw new Error(`${want} not in: ${text}`);
        }
    }
    if (
        (await page.getByText("Change Password").count()) !== 0 ||
        (await page.getByText("2FA Settings").count()) !== 0
    ) {
        throw new Error("the password or two-factor settings are shown");
    }
    await page.screenshot({ path: path.join(shots, "security-github.png"), fullPage: true });
});

await step("the server refuses a password or two-factor change", async () => {
    const codes = [];
    for (const [url, data] of [
        ["/api/auth/change-password", { currentPassword: "", newPassword: "a long new password" }],
        ["/api/auth/two-factor/enable", { password: "" }],
    ]) {
        const res = await page.request.post(url, { data });
        const body = await res.json();
        if (res.status() !== 400 || body.code !== "GITHUB_ACCOUNT") {
            throw new Error(`${url}: ${res.status()} ${JSON.stringify(body)}`);
        }
        codes.push(body.code);
    }
    return codes.join(", ");
});

await step("a password account's Security page is unchanged", async () => {
    await context.clearCookies();
    await page.goto("/dashboard");
    await page.getByPlaceholder("Username").fill("admin");
    await page.getByPlaceholder("Password").fill(password);
    await page.getByRole("button", { name: "Log in" }).click();
    await page.waitForSelector("text=Add New Monitor");
    await page.goto("/settings/security");
    await page.getByText("Change Password").waitFor();
    await page.getByText("2FA Settings").waitFor();
    if ((await page.getByTestId("github-account").count()) !== 0) {
        throw new Error("the GitHub section is shown");
    }
});

await browser.close();
github.close();
const passed = results.filter(Boolean).length;
console.log(`${passed}/${results.length} passed; page errors: ${errors.length}`);
for (const e of errors.slice(0, 5)) {
    console.log("page error:", e);
}
process.exit(passed === results.length ? 0 : 1);
