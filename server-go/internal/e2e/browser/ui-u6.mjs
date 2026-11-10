// U6 against the built image: importing monitors from CSV and YAML, editing the selected
// monitors together, and the Edit Monitor form's Test button.
//
// Usage: node ui-u6.mjs <playwright-core dir> <chromium> <base URL> <screenshot dir>
//
// It needs a new database. The monitors check the server's own /healthz.
import { createRequire } from "node:module";
import path from "node:path";

const [pwDir, chromiumPath, baseURL, shots] = process.argv.slice(2);
const require = createRequire(path.join(pwDir, "index.js"));
const { chromium } = require(pwDir);
const password = "Kuma-E2E-U6-v5Jd8Qx1";
// The server, as the server itself reaches it
const health = new URL("/healthz", baseURL).href;

const browser = await chromium.launch({ executablePath: chromiumPath });
const context = await browser.newContext({ baseURL, viewport: { width: 1400, height: 1000 }, timezoneId: "UTC" });
const page = await context.newPage();
const errors = [];
page.on("pageerror", (e) => errors.push(e.message));
page.on("console", (m) => m.type() === "error" && /TypeError|ReferenceError/.test(m.text()) && errors.push(m.text()));
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
 * Send a realtime event from the page, and return its answer
 * @param {...any} args Event and arguments
 * @returns {Promise<object>} Answer
 */
const emit = (...args) =>
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
 * The monitors the page knows, by name
 * @returns {Promise<object>} Monitors
 */
const monitorsByName = () =>
    page.evaluate(() =>
        Object.fromEntries(
            Object.values(document.querySelector("#app").__vue_app__._container._vnode.component.proxy.monitorList).map(
                (m) => [m.name, m]
            )
        )
    );

/**
 * Wait for a Bootstrap dialog to be shown, and for its fade
 * @param {string} testid The dialog's test id
 * @returns {Promise<object>} The dialog
 */
async function dialog(testid) {
    const d = page.getByTestId(testid);
    for (let i = 0; i < 50 && !(await d.isVisible()); i++) {
        await page.waitForTimeout(100);
    }
    expect(await d.isVisible(), `${testid} isn't shown`);
    await page.waitForTimeout(400);
    return d;
}

/**
 * Close the dialog that's open, and wait for its fade
 * @param {object} d The dialog
 * @returns {Promise<void>}
 */
async function close(d) {
    await d.locator(".btn-close").click();
    for (let i = 0; i < 30 && (await d.isVisible()); i++) {
        await page.waitForTimeout(100);
    }
    await page.waitForTimeout(300);
}

let notifications;
let tags;

await step("set up an account, two notifications and two tags", async () => {
    const setup = await page.request.post("/api/setup", { data: { username: "admin", password } });
    expect((await setup.json()).ok === true, "setup failed");
    await page.goto("/dashboard");
    await page.getByPlaceholder("Username").fill("admin");
    await page.getByPlaceholder("Password").fill(password);
    await page.getByRole("button", { name: "Log in" }).click();
    await page.getByText("Quick Stats").waitFor();
    notifications = {};
    for (const name of ["Old hook", "Team hook"]) {
        const r = await emit(
            "addNotification",
            { name, type: "webhook", webhookURL: "http://127.0.0.1:1/hook", isDefault: name === "Old hook" },
            null
        );
        expect(r.ok, JSON.stringify(r));
        notifications[name] = r.id;
    }
    tags = {};
    for (const [name, color] of [
        ["env", "#2563eb"],
        ["legacy", "#dc2626"],
    ]) {
        const r = await emit("addTag", { name, color });
        expect(r.ok, JSON.stringify(r));
        tags[name] = r.tag.id;
    }
    const group = await emit("add", {
        type: "group",
        name: "shop",
        interval: 60,
        notificationIDList: {},
        accepted_statuscodes: ["200-299"],
    });
    expect(group.ok, JSON.stringify(group));
    return `notifications ${JSON.stringify(notifications)}, tags ${JSON.stringify(tags)}`;
});

await step("a CSV with a mistake is refused, row by row, and nothing is added", async () => {
    await page.getByTestId("import-monitors-link").click();
    const d = await dialog("import-dialog");
    const label = (await d.getByTestId("import-run").innerText()).trim();
    expect(label === "Import", `before a check: "${label}"`);
    const csv = [
        "name,type,url,interval,acceptedStatusCodes",
        `alpha,http,${health},60,200-299`,
        `beta,keyword,${health},30,`,
        `gamma,http,${health},sixty,`,
    ].join("\n");
    await d.getByTestId("import-text").fill(csv);
    expect((await d.getByText("Read as CSV.").count()) === 1, "not read as CSV");
    await d.getByTestId("import-check").click();
    await d.getByTestId("import-errors").waitFor({ timeout: 5000 });
    const local = await d.getByTestId("import-errors").innerText();
    expect(local.includes('Row 3: interval must be a number, not "sixty"'), local);
    // A field the server knows is wrong: keyword without a keyword
    await d.getByTestId("import-text").fill(csv.replace(",sixty,", ",60,"));
    await d.getByTestId("import-check").click();
    await d.getByTestId("import-rows").waitFor({ timeout: 5000 });
    const message = await d.getByTestId("import-message").innerText();
    expect(/1 of 3 monitors can't be imported/.test(message), message);
    const bad = await d.locator('[data-testid="import-row"].table-danger').innerText();
    expect(bad.includes("beta"), `the bad row: ${bad}`);
    expect(await d.getByTestId("import-run").isDisabled(), "Import is enabled");
    expect(!("alpha" in (await monitorsByName())), "alpha was added");
    return `${local.trim()} / ${message.trim()} / ${bad.replace(/\s+/g, " ").trim()}`;
});

await step("a valid CSV is checked, then imported", async () => {
    const d = page.getByTestId("import-dialog");
    const csv = [
        "name,type,url,interval,keyword,acceptedStatusCodes",
        `alpha,http,${health},60,,200-299`,
        `beta,keyword,${health},30,ok,`,
        `gamma,http,${health},60,,200-299;301`,
    ].join("\n");
    await d.getByTestId("import-text").fill(csv);
    await d.getByTestId("import-check").click();
    await d.getByText("All 3 monitors are valid").waitFor({ timeout: 5000 });
    expect((await d.getByText("Ready", { exact: true }).count()) === 3, "rows not marked ready");
    // Changing the text needs another check
    await d.getByTestId("import-text").press("End");
    await d.getByTestId("import-text").type(" ");
    expect(await d.getByTestId("import-run").isDisabled(), "Import enabled after an edit");
    await d.getByTestId("import-text").fill(csv);
    await d.getByTestId("import-check").click();
    await d.getByText("All 3 monitors are valid").waitFor({ timeout: 5000 });
    await d.getByTestId("import-run").click();
    await d.getByText("Imported 3 monitors.").first().waitFor({ timeout: 10000 });
    expect((await d.getByRole("link", { name: "Added" }).count()) === 3, "rows not linked");
    expect(await d.getByTestId("import-run").isDisabled(), "Import could run twice");
    await page.waitForFunction(
        () =>
            ["alpha", "beta", "gamma"].every((n) =>
                Object.values(
                    document.querySelector("#app").__vue_app__._container._vnode.component.proxy.monitorList
                ).some((m) => m.name === n)
            ),
        null,
        { timeout: 10000 }
    );
    const m = await monitorsByName();
    expect(m.beta.interval === 30 && m.beta.keyword === "ok" && m.beta.type === "keyword", JSON.stringify(m.beta));
    expect(
        JSON.stringify(m.gamma.accepted_statuscodes) === JSON.stringify(["200-299", "301"]),
        JSON.stringify(m.gamma.accepted_statuscodes)
    );
    expect(m.alpha.notificationIDList?.[notifications["Old hook"]] === true, "no default notification");
    await close(d);
});

await step("YAML from a file, into a group", async () => {
    await page.goto("/add?import=1");
    const d = await dialog("import-dialog");
    const group = (await monitorsByName()).shop.id;
    const yaml = `monitors:\n  - name: delta\n    type: http\n    url: ${health}\n    parentId: ${group}\n`;
    await d.getByTestId("import-file").setInputFiles({
        name: "more.yaml",
        mimeType: "application/yaml",
        buffer: Buffer.from(yaml),
    });
    await page.waitForFunction(
        () => document.querySelector('[data-testid="import-text"]').value.includes("delta"),
        null,
        { timeout: 5000 }
    );
    expect((await d.getByText("Read as YAML.").count()) === 1, "not read as YAML");
    await d.getByTestId("import-check").click();
    await d.getByText("The monitor is valid.").waitFor({ timeout: 5000 });
    await d.getByTestId("import-run").click();
    await d.getByText("Imported 1 monitor.").first().waitFor({ timeout: 10000 });
    await page.waitForFunction(
        () =>
            Object.values(
                document.querySelector("#app").__vue_app__._container._vnode.component.proxy.monitorList
            ).some((m) => m.name === "delta"),
        null,
        { timeout: 10000 }
    );
    const delta = (await monitorsByName()).delta;
    expect(delta.parent === group, `parent ${delta.parent}, not ${group}`);
    await close(d);
});

await step("the selected monitors get an interval, a notification and a tag together", async () => {
    await page.goto("/dashboard");
    // The box is replaced by "select all" once clicked
    await page.getByTestId("select-mode-toggle").click();
    // Everything is selected at first: keep only alpha, beta and gamma
    const m = await monitorsByName();
    await page.evaluate(
        (keep) => {
            const find = (vnode) => {
                if (!vnode) {
                    return null;
                }
                const c = vnode.component;
                if (c) {
                    if (c.proxy && "selectedMonitors" in c.proxy) {
                        return c.proxy;
                    }
                    return find(c.subTree);
                }
                if (Array.isArray(vnode.children)) {
                    for (const child of vnode.children) {
                        const f = find(child);
                        if (f) {
                            return f;
                        }
                    }
                }
                return null;
            };
            const list = find(document.querySelector("#app").__vue_app__._container._vnode);
            list.selectedMonitors = Object.fromEntries(keep.map((id) => [id, true]));
        },
        [m.alpha.id, m.beta.id, m.gamma.id]
    );
    await page.getByTestId("bulk-actions").click();
    await page.getByTestId("bulk-edit").click();
    const d = await dialog("bulk-edit-dialog");
    expect((await d.locator(".modal-title").innerText()).trim() === "Edit 3 monitors", "title");
    const old = d.locator('[data-testid="bulk-notification"][data-name="Old hook"]');
    expect((await old.innerText()).includes("3 have it"), await old.innerText());
    expect(await d.getByTestId("bulk-edit-save").isDisabled(), "save enabled with nothing changed");
    await d.getByTestId("bulk-interval").fill("45");
    await d.locator('[data-testid="bulk-notification"][data-name="Team hook"] select').selectOption("add");
    await old.locator("select").selectOption("remove");
    const env = d.locator('[data-testid="bulk-tag"][data-name="env"]');
    await env.locator("select").selectOption("add");
    await env.locator("input").fill("prod");
    const summary = (await d.getByTestId("bulk-edit-summary").innerText()).trim();
    expect(summary === "interval 45 s; add Team hook; remove Old hook; add env", `summary "${summary}"`);
    await d.getByTestId("bulk-edit-save").click();
    await page.getByText("Changed 3 monitors.").first().waitFor({ timeout: 10000 });
    await page.waitForFunction(
        (id) =>
            document.querySelector("#app").__vue_app__._container._vnode.component.proxy.monitorList[id]?.interval ===
            45,
        m.alpha.id,
        { timeout: 10000 }
    );
    const after = await monitorsByName();
    for (const name of ["alpha", "beta", "gamma"]) {
        const x = after[name];
        expect(x.interval === 45, `${name} interval ${x.interval}`);
        expect(x.notificationIDList[notifications["Team hook"]] === true, `${name} lacks Team hook`);
        expect(!x.notificationIDList[notifications["Old hook"]], `${name} kept Old hook`);
        expect(
            x.tags.some((t) => t.tag_id === tags.env && t.value === "prod"),
            `${name} tags ${JSON.stringify(x.tags)}`
        );
    }
    expect(after.delta.interval === 60 && after.delta.notificationIDList[notifications["Old hook"]], "delta changed");
    expect(after.beta.keyword === "ok" && after.gamma.accepted_statuscodes.length === 2, "other settings changed");
    expect((await page.getByTestId("bulk-actions").count()) === 0, "still selecting");
    return summary;
});

await step("a value one monitor refuses changes none, and a tag comes off", async () => {
    const m = await monitorsByName();
    const r = await emit("bulkEditMonitors", [m.alpha.id, m.beta.id], { interval: 0, removeTags: [tags.env] });
    expect(r.ok === false && /interval/.test(r.msg), JSON.stringify(r));
    expect((await monitorsByName()).alpha.tags.length === 1, "the tag came off anyway");
    const ok = await emit("bulkEditMonitors", [m.alpha.id], { removeTags: [tags.env] });
    expect(ok.ok, JSON.stringify(ok));
    await page.waitForFunction(
        (id) =>
            document.querySelector("#app").__vue_app__._container._vnode.component.proxy.monitorList[id]?.tags
                .length === 0,
        m.alpha.id,
        { timeout: 10000 }
    );
    expect((await monitorsByName()).beta.tags.length === 1, "beta lost its tag");
    return r.msg;
});

await step("Test runs one check of the form, without saving", async () => {
    const before = Object.keys(await monitorsByName()).length;
    await page.goto("/add");
    await page.getByTestId("monitor-type-select").selectOption("http");
    await page.getByTestId("friendly-name-input").fill("trial");
    await page.getByTestId("url-input").fill(health);
    await page.getByTestId("test-button").click();
    const result = page.getByTestId("test-result");
    await result.waitFor({ timeout: 20000 });
    const up = (await result.innerText()).replace(/\s+/g, " ").trim();
    expect(/^Up \d+ ms 200 - OK/.test(up), `up: "${up}"`);
    await page.getByTestId("url-input").fill("http://127.0.0.1:1/");
    await page.getByTestId("test-button").click();
    await page.waitForFunction(
        () => document.querySelector('[data-testid="test-result"]')?.textContent.includes("Down"),
        null,
        { timeout: 20000 }
    );
    const down = (await result.innerText()).replace(/\s+/g, " ").trim();
    expect(/refused/i.test(down), `down: "${down}"`);
    // An invalid form isn't tested
    await page.getByTestId("url-input").fill("");
    await page.getByTestId("test-button").click();
    await page.waitForTimeout(500);
    expect((await page.getByTestId("url-input").evaluate((el) => el.validity.valid)) === false, "empty URL valid");
    expect(new URL(page.url()).pathname === "/add", "left the form");
    expect(Object.keys(await monitorsByName()).length === before, "the test saved a monitor");
    await page.getByTestId("monitor-type-select").selectOption("push");
    expect((await page.getByTestId("test-button").count()) === 0, "Test shown for push");
    return `${up} / ${down}`;
});

await step("the command palette opens the import", async () => {
    await page.goto("/dashboard");
    await page.getByText("Quick Stats").waitFor();
    await page.keyboard.press("Control+k");
    const input = page.getByTestId("command-palette-input");
    await page.waitForFunction(() => document.activeElement?.dataset.testid === "command-palette-input");
    await input.fill("import mon");
    await page.keyboard.press("Enter");
    await page.waitForURL(/\/add\?import=1/);
    await dialog("import-dialog");
});

await page.screenshot({ path: path.join(shots, "u6-import.png") });
await close(page.getByTestId("import-dialog"));
await browser.close();
const passed = results.filter(Boolean).length;
console.log(`${passed}/${results.length} passed; page errors: ${errors.length}`);
for (const e of errors.slice(0, 5)) {
    console.log("page error:", e);
}
process.exit(passed === results.length ? 0 : 1);
