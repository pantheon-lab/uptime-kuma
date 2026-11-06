// Adds a Docker host and a remote browser in the settings, creates one monitor of each Phase 4d type
// through the web UI's form, reads its first beat through the REST API, and checks that editing
// shows the saved settings.
//
// Usage: node monitor-types-4d.mjs <playwright-core dir> <chromium> <base URL> <admin token> <screenshot dir>
//
// It needs a new database and, on this machine, what server-go/README.md describes: the Docker
// socket and the system D-Bus socket mounted in the server's container, an SFTP server, a gRPC
// health server and a Chrome with a DevTools port. The server must use the host's network.
import { createRequire } from "node:module";
import path from "node:path";

const [pwDir, chromiumPath, baseURL, token, shots] = process.argv.slice(2);
const require = createRequire(path.join(pwDir, "index.js"));
const { chromium } = require(pwDir);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const browser = await chromium.launch({ executablePath: chromiumPath });
const context = await browser.newContext({ baseURL, viewport: { width: 1400, height: 1100 }, timezoneId: "UTC" });
const page = await context.newPage();
const errors = [];
page.on("pageerror", (e) => errors.push(e.message));

await page.request.post("/api/setup", { data: { username: "admin", password: "Kuma-E2E-8f4Q2xR9p" } });
await page.goto("/dashboard");
await page.getByPlaceholder("Username").fill("admin");
await page.getByPlaceholder("Password").fill("Kuma-E2E-8f4Q2xR9p");
await page.getByRole("button", { name: "Log in" }).click();
await page.waitForSelector("text=Add New Monitor");

/**
 * Wait for a toast with the text
 * @param {string} text Text the toast starts with
 * @returns {Promise<void>}
 */
async function toast(text) {
    try {
        await page.getByText(text).first().waitFor({ timeout: 15000 });
    } catch (e) {
        await page.screenshot({ path: path.join(shots, "fail-toast.png"), fullPage: true });
        throw new Error(
            `no toast "${text}"; the page says: ${(await page.locator("body").innerText()).slice(0, 2000)}`
        );
    }
}

// The settings pages' dialogs, with their test buttons.
await page.goto("/settings/docker-hosts");
await page.getByRole("button", { name: "Set Up Docker Host" }).click();
await page.locator("#docker-name").fill("local");
await page.locator("#docker-type").selectOption("socket");
await page.locator("#docker-daemon").fill("/var/run/docker.sock");
await page.locator(".modal.show .btn-warning").click();
await toast("Connected Successfully. Amount of containers:");
await page.locator(".modal.show button[type=submit]").click();
await toast("Saved");
console.log("PASS docker host: tested and saved");

await page.goto("/settings/remote-browsers");
await page.locator(".btn-primary", { hasText: "Remote Browser" }).first().click();
await page.locator("#remote-browser-name").fill("chrome");
await page.locator("#remote-browser-url").fill("http://127.0.0.1:59222");
await page.locator(".modal.show .btn-warning").click();
await toast("Connected Successfully.");
await page.locator(".modal.show button[type=submit]").click();
await toast("Saved");
console.log("PASS remote browser: tested and saved");

/**
 * Add a monitor through the form
 * @param {string} type Monitor type
 * @param {string} name Friendly name
 * @param {Function} fill Fills the type's fields
 * @param {Function} submit Saves the form, if not with the save button
 * @returns {Promise<string>} Monitor id
 */
async function add(type, name, fill, submit) {
    await page.goto("/add");
    await page.getByTestId("monitor-type-select").selectOption(type);
    await page.getByTestId("friendly-name-input").fill(name);
    await fill();
    await (submit ? submit() : page.getByTestId("save-button").click());
    await page.waitForURL(/\/dashboard\/\d+/, { timeout: 15000 });
    return page.url().split("/").pop();
}

/**
 * Wait for a monitor's first beat
 * @param {string} id Monitor id
 * @returns {Promise<object>} The beat
 */
async function firstBeat(id) {
    for (let i = 0; i < 60; i++) {
        const res = await page.request.get(`/api/v1/monitors/${id}/beats?limit=1`, {
            headers: { Authorization: `Bearer ${token}` },
        });
        const beats = await res.json();
        const list = Array.isArray(beats) ? beats : beats.beats || [];
        if (list.length > 0) {
            return list[0];
        }
        await sleep(500);
    }
    throw new Error(`no beat for monitor ${id}`);
}

const cases = [
    {
        type: "docker",
        name: "TimescaleDB container",
        fill: async () => {
            await page.locator("#docker_container").fill("server-go-timescaledb-1");
            await page.locator("#docker-host").selectOption({ label: "local" });
        },
        want: (b) => b.status === 1 && b.msg === "healthy",
        reopen: async () => (await page.locator("#docker_container").inputValue()) === "server-go-timescaledb-1",
    },
    {
        type: "system-service",
        name: "Docker service",
        fill: async () => {
            await page.locator("#system-service-name").fill("docker");
        },
        want: (b) => b.status === 1 && b.msg === "Service 'docker' is running.",
        reopen: async () => (await page.locator("#system-service-name").inputValue()) === "docker",
    },
    {
        type: "pm2",
        name: "PM2 without PM2",
        fill: async () => {
            await page.locator("#pm2-process-name").fill("api");
        },
        // The image has no pm2 command.
        want: (b) => b.status === 0 && b.msg === "Unable to query PM2 process list (ENOENT).",
        reopen: async () => (await page.locator("#pm2-process-name").inputValue()) === "api",
    },
    {
        type: "sftp",
        name: "SFTP",
        fill: async () => {
            await page.locator("#hostname").fill("127.0.0.1");
            await page.locator("#port").fill("52222");
            await page.locator("#ssh_username").fill("kuma");
            await page.locator("#ssh_password input, input#ssh_password").first().fill("kumapass");
            await page.locator("#sftp_path").fill("/upload");
        },
        want: (b) => b.status === 1 && b.msg === "SFTP connection successful",
        reopen: async () => (await page.locator("#sftp_path").inputValue()) === "/upload",
    },
    {
        type: "grpc-keyword",
        name: "gRPC health",
        fill: async () => {
            await page.locator("#grpc-url").fill("127.0.0.1:50051");
            await page.locator("#keyword").fill("SERVING");
            await page.getByPlaceholder("Example: Health").fill("Health");
            await page.getByPlaceholder("Example: check").fill("check");
            await page
                .locator("#protobuf")
                .fill(
                    'syntax = "proto3";\npackage grpc.health.v1;\nservice Health {\n  rpc Check(HealthCheckRequest) returns (HealthCheckResponse);\n}\n' +
                        "message HealthCheckRequest {\n  string service = 1;\n}\n" +
                        "message HealthCheckResponse {\n  enum ServingStatus {\n    UNKNOWN = 0;\n    SERVING = 1;\n  }\n  ServingStatus status = 1;\n}\n"
                );
            await page.locator("textarea#body").fill('{"service": ""}');
        },
        want: (b) => b.status === 1 && b.msg === '{"status":"SERVING"}, keyword [SERVING] is found',
        reopen: async () => (await page.locator("#grpc-url").inputValue()) === "127.0.0.1:50051",
    },
    {
        type: "real-browser",
        name: "Real browser",
        fill: async () => {
            await page.getByTestId("url-input").fill("https://example.com");
            await page.locator("#toggle").check();
        },
        want: (b) => b.status === 1 && b.msg === "200",
        reopen: async () =>
            (await page.locator("#toggle").isChecked()) &&
            (await page.getByTestId("url-input").inputValue()) === "https://example.com",
        // The details page shows the screenshot.
        after: async (id) => {
            await page.goto(`/dashboard/${id}`);
            const image = page.locator('img[alt="screenshot of the website"]');
            await image.waitFor({ timeout: 15000 });
            await page.waitForFunction((img) => img.complete && img.naturalWidth > 0, await image.elementHandle(), {
                timeout: 15000,
            });
            const width = await image.evaluate((img) => img.naturalWidth);
            if (width !== 1280) {
                throw new Error(`the screenshot is ${width} pixels wide`);
            }
        },
    },
];

let failed = 0;
for (const c of cases) {
    try {
        const id = await add(c.type, c.name, c.fill, c.submit);
        const beat = await firstBeat(id);
        if (!c.want(beat)) {
            throw new Error(`beat ${JSON.stringify(beat)}`);
        }
        await page.goto(`/edit/${id}`);
        await page.waitForSelector('[data-testid="friendly-name-input"]');
        await sleep(500);
        if (!(await c.reopen())) {
            throw new Error("the edit form doesn't show the saved settings");
        }
        if (c.after) {
            await c.after(id);
        }
        console.log(`PASS ${c.type}: ${beat.msg}`);
    } catch (e) {
        failed++;
        console.log(`FAIL ${c.type}: ${e.message}`);
        await page.screenshot({ path: path.join(shots, `fail-${c.type}.png`), fullPage: true }).catch(() => {});
    }
}
await page.goto("/dashboard");
await sleep(1500);
await page.screenshot({ path: path.join(shots, "dashboard.png"), fullPage: true });
await browser.close();
console.log(`${cases.length - failed}/${cases.length} passed; page errors: ${errors.length}`);
for (const e of errors.slice(0, 5)) {
    console.log("page error:", e);
}
process.exit(failed ? 1 : 0);
