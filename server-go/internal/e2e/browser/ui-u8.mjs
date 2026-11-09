// U8 against the built image: Bootstrap 5.3's colour mode and Font Awesome 6, menus and groups
// from the keyboard, focus after dragging a monitor into a group, and statuses shown by more than
// colour, with the heartbeat bar's contrast.
//
// Usage: node ui-u8.mjs <playwright-core dir> <chromium> <base URL> <screenshot dir> <psql command>
//
// It needs a new database. The psql command runs SQL in the service's database, to add a viewer.
import { execSync } from "node:child_process";
import { createRequire } from "node:module";
import path from "node:path";

const [pwDir, chromiumPath, baseURL, shots, psqlCommand] = process.argv.slice(2);
const require = createRequire(path.join(pwDir, "index.js"));
const { chromium } = require(pwDir);
const password = "Kuma-E2E-U8-k7Pz2Lm5";
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
    const context = await browser.newContext({
        baseURL,
        viewport: { width: 1300, height: 900 },
        timezoneId: "UTC",
        ...options,
    });
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
 * The element with focus, described
 * @param {object} page Page
 * @returns {Promise<string>} Its test id, or tag and text
 */
const focused = (page) =>
    page.evaluate(() => {
        const el = document.activeElement;
        return el?.dataset?.testid || `${el?.tagName}:${(el?.innerText || "").trim().slice(0, 30)}`;
    });

let admin;
const ids = {};

await step("set up monitors, a group, and a viewer", async () => {
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
    const group = await emit(admin, "add", {
        type: "group",
        name: "Shop group",
        interval: 20,
        notificationIDList: {},
        accepted_statuscodes: ["200-299"],
    });
    ids.group = group.monitorID;
    const r = await emit(
        admin,
        "importMonitors",
        [
            { name: "Up one", type: "http", url: health, interval: 20 },
            { name: "Down one", type: "http", url: "http://127.0.0.1:1/", interval: 20 },
            { name: "Child", type: "http", url: health, interval: 20, parentId: ids.group },
        ],
        false
    );
    expect(r.ok, JSON.stringify(r));
    for (const row of r.rows) {
        ids[row.name] = row.id;
    }
    // A few checks each, for the bar
    await admin.waitForTimeout(45000);
    return JSON.stringify(ids);
});

await step("Bootstrap 5.3's colour mode follows the theme, with the dark theme's colours", async () => {
    const read = () =>
        admin.evaluate(() => ({
            mode: document.documentElement.getAttribute("data-bs-theme"),
            bg: getComputedStyle(document.querySelector('[data-testid="monitor-search"]')).backgroundColor,
            emphasis: getComputedStyle(document.documentElement).getPropertyValue("--bs-emphasis-color").trim(),
        }));
    const light = await read();
    expect(light.mode === "light", `light mode: ${light.mode}`);
    // --bs-emphasis-color is new in 5.3
    expect(light.emphasis !== "", "no --bs-emphasis-color: not Bootstrap 5.3");
    await admin.evaluate(() => localStorage.setItem("theme", "dark"));
    await admin.reload();
    await admin.getByText("Quick Stats").waitFor();
    const dark = await read();
    expect(dark.mode === "dark", `dark mode: ${dark.mode}`);
    expect(dark.bg === "rgb(9, 12, 16)", `a dark field is ${dark.bg}`);
    await admin.evaluate(() => localStorage.setItem("theme", "light"));
    await admin.reload();
    await admin.getByText("Quick Stats").waitFor();
    return `${light.bg} / ${dark.bg}`;
});

await step("icons are Font Awesome 6's, under their old names too", async () => {
    // tachometer-alt is gauge-high in 6, times-circle is circle-xmark
    const icon = await admin.locator('.nav-link svg[data-icon="gauge-high"]').count();
    expect(icon > 0, "no gauge-high icon in the header");
    const missing = await admin.evaluate(() => [...document.querySelectorAll("svg.svg-inline--fa")].length);
    expect(missing > 5, `${missing} icons drawn`);
});

await step("the account menu opens from the keyboard, and Escape closes it", async () => {
    const toggle = admin.getByTestId("account-menu");
    expect((await toggle.evaluate((el) => el.tagName)) === "BUTTON", "not a button");
    expect(
        (await toggle.getAttribute("aria-label")) === "Account menu, admin",
        await toggle.getAttribute("aria-label")
    );
    await toggle.focus();
    await admin.keyboard.press("Enter");
    const menu = admin.locator(".dropdown-profile-pic .dropdown-menu");
    await admin.waitForFunction(() => document.querySelector(".dropdown-profile-pic .dropdown-menu.show"), null, {
        timeout: 3000,
    });
    expect((await toggle.getAttribute("aria-expanded")) === "true", "aria-expanded not set");
    await admin.keyboard.press("ArrowDown");
    const first = await focused(admin);
    await admin.keyboard.press("Escape");
    await admin.waitForTimeout(300);
    expect(!(await menu.evaluate((el) => el.classList.contains("show"))), "still open");
    return `first item: ${first}`;
});

await step("the status filter works from the keyboard", async () => {
    // The status filter's button opens its menu when it gets focus, as from Tab
    await admin.getByTestId("filter-status").focus();
    await admin.locator(".filter-dropdown-menu.open").waitFor({ timeout: 3000 });
    const at = await focused(admin);
    const down = admin.getByTestId("filter-status-down");
    await down.focus();
    expect((await down.getAttribute("role")) === "checkbox", "no checkbox role");
    expect((await down.getAttribute("aria-checked")) === "false", "checked at first");
    await admin.keyboard.press("Space");
    await admin.waitForFunction(
        () => document.querySelector('[data-testid="filter-status-down"]')?.getAttribute("aria-checked") === "true",
        null,
        { timeout: 3000 }
    );
    const rows = await admin.locator('[data-testid="monitor-list"] .draggable-item').count();
    expect(rows === 1, `${rows} rows with Down chosen`);
    await down.focus();
    await admin.keyboard.press("Enter");
    await admin.waitForFunction(
        () => document.querySelector('[data-testid="filter-status-down"]')?.getAttribute("aria-checked") === "false",
        null,
        { timeout: 3000 }
    );
    await down.focus();
    await admin.keyboard.press("Escape");
    await admin.waitForTimeout(300);
    const open = await admin.locator(".filter-dropdown-menu.open").count();
    expect(open === 0, "the menu stayed open");
    return `from ${at}`;
});

await step("a group opens and closes from the keyboard", async () => {
    const row = admin.locator(`.draggable-item[data-monitor-id="${ids.group}"]`);
    const button = row.getByTestId("monitor-collapse");
    expect((await button.evaluate((el) => el.tagName)) === "BUTTON", "not a button");
    const label = await button.getAttribute("aria-label");
    const childShown = () => admin.locator(`.draggable-item[data-monitor-id="${ids.Child}"]`).count();
    const expanded = await button.getAttribute("aria-expanded");
    await button.focus();
    await admin.keyboard.press("Enter");
    await admin.waitForTimeout(300);
    expect((await button.getAttribute("aria-expanded")) !== expanded, "aria-expanded didn't change");
    const after = await childShown();
    await admin.keyboard.press("Enter");
    await admin.waitForTimeout(300);
    expect((await childShown()) !== after, "the child didn't show or hide");
    expect(new URL(admin.url()).pathname === "/dashboard", `followed the link to ${admin.url()}`);
    return label;
});

await step("a monitor dragged into a group is shown there, with focus", async () => {
    // Close the group first, so that the drop must open it
    const button = admin.locator(`.draggable-item[data-monitor-id="${ids.group}"]`).getByTestId("monitor-collapse");
    if ((await button.getAttribute("aria-expanded")) === "true") {
        await button.click();
    }
    await admin
        .locator(`.draggable-item[data-monitor-id="${ids["Up one"]}"] a.item`)
        .dragTo(admin.locator(`.draggable-item[data-monitor-id="${ids.group}"]`));
    await admin.getByText("Moved Up one into Shop group.").first().waitFor({ timeout: 10000 });
    await admin.waitForFunction(
        (id) => document.activeElement?.closest(".draggable-item")?.dataset.monitorId === String(id),
        ids["Up one"],
        { timeout: 5000 }
    );
    expect((await button.getAttribute("aria-expanded")) === "true", "the group stayed closed");
    const parent = await admin.evaluate(
        (id) => document.querySelector("#app").__vue_app__._container._vnode.component.proxy.monitorList[id].parent,
        ids["Up one"]
    );
    expect(parent === ids.group, `parent ${parent}`);
});

await step("the uptime pill says the status by an icon and in words", async () => {
    const pill = admin.locator(`.draggable-item[data-monitor-id="${ids["Down one"]}"] .badge`);
    expect((await pill.getAttribute("data-status")) === "0", `status ${await pill.getAttribute("data-status")}`);
    expect((await pill.locator('svg[data-icon="circle-xmark"]').count()) === 1, "no icon on the down pill");
    const words = await pill.locator(".visually-hidden").innerText();
    expect(words.trim() === "Down:", `words "${words}"`);
    const up = admin.locator(`.draggable-item[data-monitor-id="${ids.Child}"] .badge`);
    expect((await up.locator("svg").count()) === 0, "an up pill has an icon");
});

await step("the heartbeat bar shows down by a notch, and up beats have a darker edge", async () => {
    await admin.goto(`/dashboard/${ids["Down one"]}`);
    await admin.waitForTimeout(2500);
    const bar = admin.locator(".hp-bar-big canvas:not(.monitor-list *)").first();
    const label = await bar.getAttribute("aria-label");
    expect(/checks, 0 up, \d+ down, 0 pending, 0 in maintenance/.test(label), label);
    // In a column through a down beat, red above and below the middle, and nothing in it
    const notch = await admin.evaluate(() => {
        const c = [...document.querySelectorAll(".hp-bar-big canvas")].find((el) => !el.closest(".monitor-list"));
        const ctx = c.getContext("2d");
        const { width, height } = c;
        const data = ctx.getImageData(0, 0, width, height).data;
        const at = (x, y) => data.slice((y * width + x) * 4, (y * width + x) * 4 + 4);
        const red = (p) => p[3] > 200 && p[0] > 180 && p[1] < 90;
        const mid = Math.floor(height / 2);
        for (let x = 0; x < width; x++) {
            if (
                red(at(x, mid - Math.floor(height / 5))) &&
                red(at(x, mid + Math.floor(height / 5))) &&
                at(x, mid)[3] === 0
            ) {
                return true;
            }
        }
        return false;
    });
    expect(notch, "no notched down beat");
    await admin.goto(`/dashboard/${ids.Child}`);
    await admin.waitForTimeout(2500);
    const edge = await admin.evaluate(() => {
        const c = [...document.querySelectorAll(".hp-bar-big canvas")].find((el) => !el.closest(".monitor-list"));
        const data = c.getContext("2d").getImageData(0, 0, c.width, c.height).data;
        // #2e9e5b, the edge, beside #5cdd8b, the fill
        let edgePixels = 0;
        for (let i = 0; i < data.length; i += 4) {
            if (
                Math.abs(data[i] - 0x2e) < 30 &&
                Math.abs(data[i + 1] - 0x9e) < 30 &&
                Math.abs(data[i + 2] - 0x5b) < 30
            ) {
                edgePixels++;
            }
        }
        return edgePixels;
    });
    expect(edge > 20, `${edge} edge pixels`);
    await admin
        .locator(".hp-bar-big:not(.monitor-list *)")
        .first()
        .screenshot({ path: path.join(shots, "u8-bar-up.png") });
    return `${label}; ${edge} edge pixels`;
});

await step("a viewer can't start dragging a monitor", async () => {
    const viewer = await login("viewer");
    current = viewer;
    const prevented = await viewer.evaluate((id) => {
        const link = document.querySelector(`.draggable-item[data-monitor-id="${id}"] a.item`);
        const e = new DragEvent("dragstart", { bubbles: true, cancelable: true, dataTransfer: new DataTransfer() });
        link.dispatchEvent(e);
        return e.defaultPrevented;
    }, ids["Down one"]);
    expect(prevented, "the drag started");
    await viewer.context().close();
});

await step("the code editor and editable text show keyboard focus", async () => {
    current = admin;
    const r = await emit(admin, "addStatusPage", "Public", "public");
    expect(r.ok, JSON.stringify(r));
    await admin.goto("/status/public?edit");
    await admin.getByTestId("edit-sidebar").waitFor({ timeout: 15000 });
    const title = admin.locator('[contenteditable="true"]').first();
    await title.focus();
    const outline = await title.evaluate((el) => [
        getComputedStyle(el).outlineStyle,
        parseFloat(getComputedStyle(el).outlineWidth),
    ]);
    expect(outline[0] === "solid" && outline[1] >= 2, `editable text outline: ${outline}`);
    const editor = admin.locator(".css-editor textarea").first();
    await editor.focus();
    const ring = await admin
        .locator(".css-editor")
        .first()
        .evaluate((el) => getComputedStyle(el).outlineStyle);
    expect(ring === "solid", `code editor outline: ${ring}`);
});

await browser.close();
const passed = results.filter(Boolean).length;
console.log(`${passed}/${results.length} passed; page errors: ${errors.length}`);
for (const e of errors.slice(0, 5)) {
    console.log("page error:", e);
}
process.exit(passed === results.length && errors.length === 0 ? 0 : 1);
