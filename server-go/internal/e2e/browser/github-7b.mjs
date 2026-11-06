// Phase 7b against the built image: logging in with GitHub through the login page, with a fake
// GitHub that this script serves, which checks PKCE as GitHub does. Team membership decides the
// role, someone in no team is refused, and with KUMA_PASSWORD_LOGIN=false only GitHub is offered.
//
// Usage: node github-7b.mjs <playwright-core dir> <chromium> <base URL> <screenshot dir> <admin token> <fake GitHub port> <full|github-only>
//
// Start the image with KUMA_GITHUB_CLIENT_ID=client, KUMA_GITHUB_CLIENT_SECRET=secret,
// KUMA_GITHUB_ORG=pantheon-lab, KUMA_GITHUB_ADMIN_TEAMS=sre, KUMA_GITHUB_VIEWER_TEAMS=support,
// and KUMA_GITHUB_URL and KUMA_GITHUB_API_URL set to http://127.0.0.1:<fake GitHub port>, on a new
// database. "github-only" is for a second run with KUMA_PASSWORD_LOGIN=false.
import { createHash, randomBytes } from "node:crypto";
import { createServer } from "node:http";
import { createRequire } from "node:module";
import path from "node:path";

const [pwDir, chromiumPath, baseURL, shots, adminToken, githubPort, mode] = process.argv.slice(2);
const require = createRequire(path.join(pwDir, "index.js"));
const { chromium } = require(pwDir);
const password = "Kuma-E2E-7b-q8Jv3Nc6";

const people = {
    alice: { id: 11, member: true, teams: ["sre"] },
    bob: { id: 12, member: true, teams: ["support"] },
    carol: { id: 13, member: true, teams: ["marketing"] },
};
// Who is "logged in to GitHub" in the fake, for the next authorization.
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

/**
 * Log in with the GitHub button, as someone
 * @param {string} who A person of the fake GitHub
 * @returns {Promise<void>}
 */
async function githubLogin(who) {
    current = who;
    await context.clearCookies();
    await page.goto("/dashboard");
    await page.locator("#github-login").click();
}

if (mode === "github-only") {
    await step("only GitHub is offered with password login off", async () => {
        await context.clearCookies();
        await page.goto("/dashboard");
        await page.locator("#github-login").waitFor();
        if ((await page.getByPlaceholder("Password").count()) !== 0) {
            throw new Error("the password form is shown");
        }
        const res = await page.request.post("/api/auth/sign-in/username", { data: { username: "admin", password } });
        if (res.status() !== 403) {
            throw new Error(`password login answered ${res.status()}`);
        }
        await githubLogin("alice");
        await page.waitForSelector("text=Add New Monitor");
        await page.screenshot({ path: path.join(shots, "github-only.png") });
    });
} else {
    await step("set up an admin and a monitor with credentials", async () => {
        const setup = await page.request.post("/api/setup", { data: { username: "admin", password } });
        if ((await setup.json()).ok !== true) {
            throw new Error("setup failed");
        }
        const monitor = await page.request.post("/api/v1/monitors", {
            headers: { Authorization: `Bearer ${adminToken}` },
            data: {
                name: "Secret site",
                type: "http",
                url: "https://example.com/?token=t0p-secret",
                interval: 60,
                active: false,
            },
        });
        if (monitor.status() !== 201) {
            throw new Error(`monitor ${monitor.status()}`);
        }
    });

    await step("the login page offers both", async () => {
        await page.goto("/dashboard");
        await page.getByPlaceholder("Password").waitFor();
        await page.locator("#github-login").waitFor();
        await page.screenshot({ path: path.join(shots, "login.png") });
    });

    await step("a member of an admin team logs in as an admin", async () => {
        await githubLogin("alice");
        await page.waitForSelector("text=Secret site");
        const [role, username] = await page.evaluate(() => {
            const r = document.querySelector("#app").__vue_app__._container._vnode.component.proxy;
            return [r.role, r.username];
        });
        if (
            role !== "admin" ||
            username !== "@alice" ||
            (await page.getByRole("link", { name: "Add New Monitor" }).count()) !== 1
        ) {
            throw new Error(`${username} as ${role}`);
        }
        return `${username} as ${role}`;
    });

    await step("a member of a viewer team logs in as a viewer", async () => {
        await githubLogin("bob");
        await page.waitForSelector("text=Secret site");
        const [role, held] = await page.evaluate(() => {
            const r = document.querySelector("#app").__vue_app__._container._vnode.component.proxy;
            return [r.role, JSON.stringify(r.monitorList)];
        });
        if (
            role !== "viewer" ||
            held.includes("t0p-secret") ||
            (await page.getByRole("link", { name: "Add New Monitor" }).count()) !== 0
        ) {
            throw new Error(`role ${role}`);
        }
        return "@bob as viewer, without the monitor's token";
    });

    await step("someone in no team is refused with a reason", async () => {
        await githubLogin("carol");
        await page.waitForSelector("text=isn't in a team that may use this server");
        const session = await (await page.request.get("/api/auth/get-session")).json();
        if (session?.user) {
            throw new Error("carol has a session");
        }
        await page.screenshot({ path: path.join(shots, "refused.png") });
    });

    await step("the password account still logs in", async () => {
        await context.clearCookies();
        await page.goto("/dashboard");
        await page.getByPlaceholder("Username").fill("admin");
        await page.getByPlaceholder("Password").fill(password);
        await page.getByRole("button", { name: "Log in" }).click();
        await page.waitForSelector("text=Add New Monitor");
    });
}

await browser.close();
github.close();
const passed = results.filter(Boolean).length;
console.log(`${passed}/${results.length} passed; page errors: ${errors.length}`);
for (const e of errors.slice(0, 5)) {
    console.log("page error:", e);
}
process.exit(passed === results.length ? 0 : 1);
