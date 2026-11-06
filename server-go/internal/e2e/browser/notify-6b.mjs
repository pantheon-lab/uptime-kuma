// Phase 6b against the built image: a nostr notification set up and tested through the web UI,
// read back from a real relay with nostr-tools, the library the Node.js server used, and the
// key the Webpush form subscribes browsers with.
//
// Usage: node notify-6b.mjs <playwright-core dir> <chromium> <base URL> <screenshot dir> <relay ws:// URL> <dir with nostr-tools installed>
//
// It needs a new database, and a relay such as scsibug/nostr-rs-relay.
import { createRequire } from "node:module";
import path from "node:path";
import { pathToFileURL } from "node:url";

const [pwDir, chromiumPath, baseURL, shots, relayURL, nostrToolsDir] = process.argv.slice(2);
const require = createRequire(path.join(pwDir, "index.js"));
const { chromium } = require(pwDir);
const nostr = await import(pathToFileURL(path.join(nostrToolsDir, "node_modules/nostr-tools/lib/esm/index.js")));
const password = "Kuma-E2E-6b-w4Kd8Pq2";

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
 * Send a realtime event from the page, as the UI does
 * @param {string} event Event
 * @returns {Promise<object>} The answer
 */
const emit = (event) =>
    page.evaluate(
        (name) =>
            new Promise((resolve) =>
                document
                    .querySelector("#app")
                    .__vue_app__._container._vnode.component.proxy.getSocket()
                    .emit(name, resolve)
            ),
        event
    );

await step("set up and log in", async () => {
    const res = await page.request.post("/api/setup", { data: { username: "admin", password } });
    if ((await res.json()).ok !== true) {
        throw new Error("setup failed");
    }
    await page.goto("/dashboard");
    await page.getByPlaceholder("Username").fill("admin");
    await page.getByPlaceholder("Password").fill(password);
    await page.getByRole("button", { name: "Log in" }).click();
    await page.waitForSelector("text=Add New Monitor");
});

const senderKey = nostr.generateSecretKey();
const recipientKey = nostr.generateSecretKey();
const senderPub = nostr.getPublicKey(senderKey);
const recipientPub = nostr.getPublicKey(recipientKey);

await step("a nostr notification tests through the form", async () => {
    await page.goto("/settings/notifications");
    await page.getByRole("button", { name: "Set Up Notification" }).click();
    const modal = page.locator(".modal.show");
    await modal.locator("#notification-type").selectOption("nostr");
    await modal.locator("#notification-name").fill("Nostr DM");
    await modal.locator("#nostr-relays").fill(relayURL);
    await modal.locator("#nostr-sender input, input#nostr-sender").first().fill(nostr.nip19.nsecEncode(senderKey));
    await modal.locator("#nostr-recipients").fill(nostr.nip19.npubEncode(recipientPub));
    await modal.getByRole("button", { name: "Test" }).click();
    await page.waitForSelector("text=Sent Successfully.");
    await modal.getByRole("button", { name: "Save" }).click();
    await page.waitForSelector("text=Nostr DM");
});

await step("nostr-tools reads the message from the relay as the recipient", async () => {
    const relay = await nostr.Relay.connect(relayURL);
    const wraps = await new Promise((resolve) => {
        const found = [];
        const sub = relay.subscribe([{ kinds: [1059], "#p": [recipientPub] }], {
            onevent: (event) => found.push(event),
            oneose: () => {
                sub.close();
                resolve(found);
            },
        });
    });
    relay.close();
    if (wraps.length !== 1) {
        throw new Error(`${wraps.length} gift wraps for the recipient`);
    }
    if (!nostr.verifyEvent(wraps[0]) || wraps[0].pubkey === senderPub) {
        throw new Error("the gift wrap isn't signed with a key of its own");
    }
    const rumor = nostr.nip59.unwrapEvent(wraps[0], recipientKey);
    if (rumor.kind !== 14 || rumor.pubkey !== senderPub || !rumor.content) {
        throw new Error(JSON.stringify(rumor));
    }
    return JSON.stringify(rumor.content.slice(0, 60));
});

await step("the Webpush form gets a lasting public key, and never the private one", async () => {
    const first = await emit("getWebpushVapidPublicKey");
    const again = await emit("getWebpushVapidPublicKey");
    const raw = Buffer.from(first.msg ?? "", "base64url");
    if (first.ok !== true || raw.length !== 65 || raw[0] !== 4 || again.msg !== first.msg) {
        throw new Error(`${JSON.stringify(first)} then ${JSON.stringify(again)}`);
    }
    const settings = await emit("getSettings");
    if ("webpushPrivateVapidKey" in settings.data || settings.data.webpushPublicVapidKey !== first.msg) {
        throw new Error(`settings: ${Object.keys(settings.data)}`);
    }
    await page.goto("/settings/notifications");
    await page.getByRole("button", { name: "Set Up Notification" }).click();
    await page.locator(".modal.show #notification-type").selectOption("Webpush");
    await page.screenshot({ path: path.join(shots, "webpush-form.png") });
    return "a P-256 key of 65 bytes";
});

await browser.close();
const passed = results.filter(Boolean).length;
console.log(`${passed}/${results.length} passed; page errors: ${errors.length}`);
for (const e of errors.slice(0, 5)) {
    console.log("page error:", e);
}
process.exit(passed === results.length ? 0 : 1);
