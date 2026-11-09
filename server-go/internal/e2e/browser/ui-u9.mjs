// U9 against the built image: a status page's subscribers (email, confirmed from a link, and
// webhooks), incidents sent to them and their resolution, unsubscribing from an email's link,
// incident templates, and banners for coming maintenance in the reader's time.
//
// Usage: node ui-u9.mjs <playwright-core dir> <chromium> <base URL> <screenshot dir> <psql command>
//
// It needs a new database. The psql command runs SQL in the service's database, to add a viewer
// and set the server's address. The service must reach this machine's 127.0.0.1, where the script
// runs a mail server and a webhook receiver.
import { execSync } from "node:child_process";
import http from "node:http";
import { createRequire } from "node:module";
import net from "node:net";
import path from "node:path";

const [pwDir, chromiumPath, baseURL, shots, psqlCommand] = process.argv.slice(2);
const require = createRequire(path.join(pwDir, "index.js"));
const { chromium } = require(pwDir);
const password = "Kuma-E2E-U9-q3Wm8Rt2";

// A mail server that keeps what it's sent
const mails = [];
const smtp = net.createServer((sock) => {
    sock.setEncoding("utf8");
    let buf = "";
    let inData = false;
    let rcpt = [];
    sock.write("220 sink ESMTP\r\n");
    sock.on("data", (chunk) => {
        buf += chunk;
        for (;;) {
            if (inData) {
                const end = buf.indexOf("\r\n.\r\n");
                if (end < 0) {
                    return;
                }
                mails.push({ to: rcpt, data: buf.slice(0, end) });
                buf = buf.slice(end + 5);
                inData = false;
                rcpt = [];
                sock.write("250 OK\r\n");
                continue;
            }
            const i = buf.indexOf("\r\n");
            if (i < 0) {
                return;
            }
            const line = buf.slice(0, i);
            buf = buf.slice(i + 2);
            const cmd = line.slice(0, 4).toUpperCase();
            if (cmd === "EHLO" || cmd === "HELO") {
                sock.write("250 sink\r\n");
            } else if (cmd === "RCPT") {
                rcpt.push(line.match(/<(.*)>/)?.[1]);
                sock.write("250 OK\r\n");
            } else if (cmd === "DATA") {
                inData = true;
                sock.write("354 go on\r\n");
            } else if (cmd === "QUIT") {
                sock.end("221 bye\r\n");
            } else {
                sock.write("250 OK\r\n");
            }
        }
    });
});
await new Promise((resolve) => smtp.listen(0, "127.0.0.1", resolve));

// A webhook receiver
const hooks = [];
const hookServer = http.createServer((req, res) => {
    let body = "";
    req.on("data", (c) => (body += c));
    req.on("end", () => {
        hooks.push(JSON.parse(body));
        res.end("ok");
    });
});
await new Promise((resolve) => hookServer.listen(0, "127.0.0.1", resolve));

/**
 * A message's decoded text
 * @param {object} mail Message
 * @returns {string} Headers and body, quoted-printable decoded
 */
const decode = (mail) =>
    mail.data.replace(/=\r\n/g, "").replace(/=([0-9A-F]{2})/g, (_, h) => String.fromCharCode(parseInt(h, 16)));

const browser = await chromium.launch({ executablePath: chromiumPath });
const errors = [];
const results = [];
let current;

/**
 * Collect a page's errors
 * @param {object} page Page
 * @param {string} who Whose page
 * @returns {object} The page
 */
function watch(page, who) {
    page.on("pageerror", (e) => errors.push(`${who}: ${e.message}`));
    page.on(
        "console",
        (m) => m.type() === "error" && /TypeError|ReferenceError/.test(m.text()) && errors.push(`${who}: ${m.text()}`)
    );
    return page;
}

/**
 * A new browser profile, logged in
 * @param {string} user Account
 * @returns {Promise<object>} Page
 */
async function login(user) {
    const context = await browser.newContext({ baseURL, viewport: { width: 1300, height: 900 }, timezoneId: "UTC" });
    const page = watch(await context.newPage(), user);
    await page.goto("/dashboard");
    await page.getByPlaceholder("Username").fill(user);
    await page.getByPlaceholder("Password").fill(password);
    await page.getByRole("button", { name: "Log in" }).click();
    await page.getByText("Quick Stats").waitFor({ timeout: 20000 });
    return page;
}

/**
 * A new browser profile that isn't logged in
 * @param {object} options Context options
 * @returns {Promise<object>} Page
 */
async function visitor(options = {}) {
    const context = await browser.newContext({
        baseURL,
        viewport: { width: 1300, height: 900 },
        timezoneId: "UTC",
        ...options,
    });
    return watch(await context.newPage(), "visitor");
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
 * Wait until a condition holds
 * @param {Function} cond Condition
 * @param {string} what What's awaited
 * @returns {Promise<void>}
 */
async function until(cond, what) {
    const deadline = Date.now() + 20000;
    while (!cond()) {
        if (Date.now() > deadline) {
            throw new Error(`timed out waiting for ${what}`);
        }
        await new Promise((r) => setTimeout(r, 200));
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

const inUTC = (hours) => new Date(Date.now() + hours * 3600e3).toISOString().slice(0, 16);

let admin;
let viewer;
let guest;
let unsubscribeLink;

await step("set up a page, an email notification, two coming windows and a viewer", async () => {
    const setup = await fetch(new URL("/api/setup", baseURL), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ username: "admin", password }),
    });
    expect((await setup.json()).ok === true, "setup failed");
    execSync(psqlCommand, {
        input: `INSERT INTO app_user (username, password_hash, role) SELECT 'viewer', password_hash, 'viewer' FROM app_user WHERE username = 'admin';
INSERT INTO app_setting (key, value) VALUES ('primaryBaseURL', '"${baseURL}"') ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;`,
    });
    admin = await login("admin");
    current = admin;
    const n = await emit(
        admin,
        "addNotification",
        {
            name: "Status mail",
            type: "smtp",
            smtpHost: "127.0.0.1",
            smtpPort: smtp.address().port,
            smtpFrom: "Status <status@example.com>",
            smtpTo: "ops@example.com",
            smtpIgnoreSTARTTLS: true,
            isDefault: false,
        },
        null
    );
    expect(n.ok, JSON.stringify(n));
    const p = await emit(admin, "addStatusPage", "Public", "public");
    expect(p.ok, JSON.stringify(p));
    const page = await emit(admin, "getStatusPage", "public");
    for (const [title, start] of [
        ["Database upgrade", 3],
        ["Data centre move", 240],
    ]) {
        const w = await emit(admin, "addMaintenance", {
            title,
            description: "Orders **may be slow**.",
            strategy: "single",
            active: 1,
            intervalDay: 1,
            dateRange: [inUTC(start), inUTC(start + 2)],
            timeRange: [
                { hours: 0, minutes: 0 },
                { hours: 0, minutes: 0 },
            ],
            weekdays: [],
            daysOfMonth: [],
            timezoneOption: "UTC",
        });
        expect(w.ok, JSON.stringify(w));
        const link = await emit(admin, "addMaintenanceStatusPage", w.maintenanceID, [{ id: page.config.id }]);
        expect(link.ok, JSON.stringify(link));
    }
});

await step("the editor's Subscribers dialog turns on email and adds a webhook", async () => {
    current = admin;
    await admin.goto("/status/public?edit");
    await admin.getByTestId("edit-sidebar").waitFor({ timeout: 15000 });
    await admin.getByTestId("subscribers-button").click();
    const dialog = admin.getByTestId("subscribers-dialog");
    await dialog.getByTestId("subscribers-sender").waitFor();
    const off = await dialog.getByTestId("subscribers-sender").evaluate((el) => el.selectedOptions[0].text.trim());
    expect(off === "Off: no one can subscribe by email", `sender: ${off}`);
    await dialog.getByTestId("subscribers-sender").selectOption({ label: "Status mail" });
    await dialog.getByText("Visitors can subscribe on the page").waitFor();
    await dialog.getByTestId("subscribers-webhook-url").fill(`http://127.0.0.1:${hookServer.address().port}/status`);
    await dialog.getByRole("button", { name: "Add" }).click();
    await dialog.getByTestId("subscriber").first().waitFor();
    const count = await dialog.getByTestId("subscribers-count").innerText();
    expect(count.includes("1 confirmed, 0 waiting"), `count: ${count}`);
    await admin.screenshot({ path: path.join(shots, "u9-subscribers-dialog.png") });
    await dialog.getByRole("button", { name: "Close" }).last().click();
    await admin.getByText("1 confirmed subscriber.").waitFor();
});

await step("a visitor subscribes, and the email's link confirms it", async () => {
    guest = await visitor();
    current = guest;
    await guest.goto("/status/public");
    const box = guest.getByTestId("subscribe-box");
    await box.waitFor({ timeout: 15000 });
    await box.getByTestId("subscribe-email").fill("reader@example.com");
    await box.getByTestId("subscribe-submit").click();
    await box.getByText("Check reader@example.com for an email").waitFor();
    await until(() => mails.length === 1, "the confirmation");
    const text = decode(mails[0]);
    expect(mails[0].to[0] === "reader@example.com", `to: ${mails[0].to}`);
    expect(text.includes("Subject: Confirm your subscription to Public"), text.slice(0, 400));
    const link = text.match(/(http\S+subscription=confirm&token=[\w-]+)/)?.[1];
    expect(link?.startsWith(baseURL), `link: ${link}`);
    await guest.goto(link);
    await guest.getByText("You're subscribed to Public").waitFor({ timeout: 15000 });
    expect(!guest.url().includes("token="), `the token stayed in the address: ${guest.url()}`);
    // Asking again sends nothing more: the address is subscribed
    await guest.reload();
    await guest.getByTestId("subscribe-email").fill("reader@example.com");
    await guest.getByTestId("subscribe-submit").click();
    await guest.getByText("Check reader@example.com").waitFor();
    await guest.waitForTimeout(1500);
    expect(mails.length === 1, `mails: ${mails.length}`);
});

await step("coming maintenance shows in the reader's time, only within a week", async () => {
    const hk = await visitor({ timezoneId: "Asia/Hong_Kong" });
    current = hk;
    await hk.goto("/status/public");
    const banner = hk.getByTestId("upcoming-maintenance");
    await banner.first().waitFor({ timeout: 15000 });
    expect((await banner.count()) === 1, `banners: ${await banner.count()}`);
    const heading = await banner.locator(".alert-heading").innerText();
    expect(heading.includes("Scheduled maintenance: Database upgrade"), heading);
    const when = await banner.getByTestId("upcoming-maintenance-time").innerText();
    expect(/GMT\+8/.test(when) && /in [23] hours/.test(when), `when: ${when}`);
    const bold = await banner.locator(".content strong").innerText();
    expect(bold === "may be slow", `description: ${bold}`);
    await hk.screenshot({ path: path.join(shots, "u9-upcoming.png") });
    return when;
});

await step("a starter fills the incident form, and saving it keeps a template", async () => {
    current = admin;
    await admin.getByTestId("create-incident-button").click();
    const form = admin.getByTestId("incident-edit");
    await form.getByTestId("incident-templates").click();
    await form.getByTestId("incident-starter").filter({ hasText: "Investigating" }).click();
    expect((await form.getByTestId("incident-title").innerText()) === "Investigating", "title not filled");
    const content = await form.getByTestId("incident-content-editable").innerText();
    expect(content.startsWith("We're investigating"), `content: ${content}`);
    await form.getByTestId("incident-title").fill("Checkout errors");
    await form.getByTestId("incident-templates").click();
    await form.getByTestId("incident-template-save").click();
    await admin.getByText("Saved.").first().waitFor();
    await form.getByRole("button", { name: "Cancel" }).click();
    await admin.getByTestId("create-incident-button").click();
    await admin.getByTestId("incident-templates").click();
    await admin.getByTestId("incident-template").filter({ hasText: "Checkout errors" }).click();
    const title = await admin.getByTestId("incident-edit").getByTestId("incident-title").innerText();
    expect(title === "Checkout errors", `title: ${title}`);
});

await step("posting with Send to subscribers sends it by email and webhook", async () => {
    const form = admin.getByTestId("incident-edit");
    const notify = form.getByTestId("incident-notify");
    expect(await notify.isChecked(), "Send to subscribers isn't ticked");
    const label = await form.locator('label[for="incident-notify"]').innerText();
    expect(label === "Send to subscribers (2)", `label: ${label}`);
    await admin.screenshot({ path: path.join(shots, "u9-incident-form.png") });
    await form.getByTestId("post-incident-button").click();
    await admin.getByText("Sending to 2 subscribers.").waitFor();
    await until(() => mails.length === 2 && hooks.length === 1, "the incident");
    const text = decode(mails[1]);
    expect(text.includes("Subject: [Public] Checkout errors"), text.slice(0, 400));
    expect(text.includes("List-Unsubscribe-Post: List-Unsubscribe=One-Click"), "no one-click unsubscribe");
    unsubscribeLink = text.match(/Unsubscribe: (http\S+)/)?.[1];
    expect(unsubscribeLink?.includes("subscription=unsubscribe"), `unsubscribe: ${unsubscribeLink}`);
    expect(
        hooks[0].event === "incident.created" && hooks[0].incident.title === "Checkout errors",
        JSON.stringify(hooks[0])
    );
});

await step("resolving the incident tells the same subscribers", async () => {
    await admin.getByTestId("incident").getByRole("button", { name: "Resolve" }).click();
    await until(() => mails.length === 3 && hooks.length === 2, "the resolution");
    expect(decode(mails[2]).includes("Subject: [Public] Resolved: Checkout errors"), decode(mails[2]).slice(0, 300));
    expect(hooks[1].event === "incident.resolved", JSON.stringify(hooks[1]));
});

await step("the email's unsubscribe link asks, then unsubscribes", async () => {
    current = guest;
    await guest.goto(unsubscribeLink);
    const action = guest.getByTestId("subscription-action");
    await action.getByText("Stop getting emails from Public?").waitFor({ timeout: 15000 });
    await action.getByTestId("unsubscribe-button").click();
    await action.getByText("You're unsubscribed").waitFor();
    current = admin;
    const r = await emit(admin, "getStatusPageSubscribers", "public");
    expect(r.ok && r.subscribers.length === 1 && r.subscribers[0].kind === "webhook", JSON.stringify(r.subscribers));
});

await step("a viewer gets neither the subscribers nor the templates", async () => {
    viewer = await login("viewer");
    current = viewer;
    const subs = await emit(viewer, "getStatusPageSubscribers", "public");
    const templates = await emit(viewer, "getIncidentTemplates");
    expect(subs.ok === false && templates.ok === false, `${JSON.stringify(subs)} ${JSON.stringify(templates)}`);
});

await step("at 375 pixels and in the dark theme, the banner and the form fit", async () => {
    const phone = await visitor({ viewport: { width: 375, height: 760 }, colorScheme: "dark" });
    current = phone;
    await phone.goto("/status/public");
    await phone.getByTestId("subscribe-box").waitFor({ timeout: 15000 });
    const scroll = await phone.evaluate(() => document.documentElement.scrollWidth);
    expect(scroll <= 375, `scrolls sideways: ${scroll}`);
    const button = await phone.getByTestId("subscribe-submit").boundingBox();
    expect(button && button.x + button.width <= 375, `button: ${JSON.stringify(button)}`);
    await phone.getByTestId("upcoming-maintenance").screenshot({ path: path.join(shots, "u9-phone-upcoming.png") });
    await phone.getByTestId("subscribe-box").screenshot({ path: path.join(shots, "u9-phone-subscribe.png") });
});

await browser.close();
smtp.close();
hookServer.close();
const passed = results.filter(Boolean).length;
console.log(`${passed}/${results.length} passed; page errors: ${errors.length}`);
for (const e of errors.slice(0, 5)) {
    console.log("page error:", e);
}
process.exit(passed === results.length && errors.length === 0 ? 0 : 1);
