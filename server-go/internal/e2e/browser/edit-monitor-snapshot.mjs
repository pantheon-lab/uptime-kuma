// A snapshot of the Edit Monitor form for every monitor type, to compare before and after changing
// how the form is built. For each type it records the visible fields (kind, id, label and value),
// then fills each with a value of its own and records the monitor the form would save, which shows
// that each field still writes where it did.
//
// Usage: node edit-monitor-snapshot.mjs <playwright-core dir> <chromium> <base URL> <output JSON>
//
// It needs a new database. Compare two outputs with diff.
import { writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";

const [pwDir, chromiumPath, baseURL, out] = process.argv.slice(2);
const require = createRequire(path.join(pwDir, "index.js"));
const { chromium } = require(pwDir);
const password = "Kuma-E2E-Snap-k4Wm9Tz2";

const browser = await chromium.launch({ executablePath: chromiumPath });
const page = await browser.newPage({ baseURL, viewport: { width: 1400, height: 1000 }, timezoneId: "UTC" });
const errors = [];
page.on("pageerror", (e) => errors.push(e.message));

const setup = await page.request.post("/api/setup", { data: { username: "admin", password } });
if ((await setup.json()).ok !== true) {
    throw new Error("setup failed");
}
await page.goto("/dashboard");
await page.getByPlaceholder("Username").fill("admin");
await page.getByPlaceholder("Password").fill(password);
await page.getByRole("button", { name: "Log in" }).click();
await page.getByText("Quick Stats").waitFor();

await page.goto("/add");
const select = page.getByTestId("monitor-type-select");
await select.waitFor();
const types = await select.locator("option").evaluateAll((os) => os.map((o) => o.value).filter(Boolean));

/**
 * The visible fields of the form, in order
 * @returns {Promise<object[]>} Fields
 */
const fields = () =>
    page.evaluate(() => {
        // The monitor list's search is a form too
        const form = document.querySelector('[data-testid="save-button"]').closest("form");
        const visible = (el) => !!(el.offsetWidth || el.offsetHeight || el.getClientRects().length);
        return [...form.querySelectorAll("input, select, textarea")]
            .filter((el) => visible(el) && el.type !== "hidden" && !el.closest(".multiselect"))
            .map((el) => {
                const label =
                    (el.id && form.querySelector(`label[for="${CSS.escape(el.id)}"]`)?.textContent) ||
                    el.closest("label")?.textContent ||
                    el.getAttribute("aria-label") ||
                    el.placeholder ||
                    "";
                return {
                    kind: el.tagName.toLowerCase() + (el.type ? `:${el.type}` : ""),
                    id: el.id || null,
                    label: label.replace(/\s+/g, " ").trim(),
                    value: el.type === "checkbox" || el.type === "radio" ? el.checked : el.value,
                    required: el.required,
                };
            });
    });

/**
 * The monitor the form holds, without fields that change on their own
 * @returns {Promise<object>} Monitor
 */
const formMonitor = () =>
    page.evaluate(() => {
        // Production builds don't mark elements with their components: search the tree
        const find = (vnode) => {
            if (!vnode) {
                return null;
            }
            const c = vnode.component;
            if (c) {
                if (c.proxy && "monitor" in c.proxy && "isAdd" in c.proxy) {
                    return c;
                }
                return find(c.subTree);
            }
            if (Array.isArray(vnode.children)) {
                for (const child of vnode.children) {
                    const found = find(child);
                    if (found) {
                        return found;
                    }
                }
            }
            return vnode.suspense ? find(vnode.suspense.activeBranch) : null;
        };
        const c = find(document.querySelector("#app").__vue_app__._container._vnode);
        const sort = (v) => {
            if (Array.isArray(v)) {
                return v.map(sort);
            }
            if (v && typeof v === "object") {
                return Object.fromEntries(
                    Object.keys(v)
                        .sort()
                        .map((k) => [k, sort(v[k])])
                );
            }
            return v;
        };
        const m = JSON.parse(JSON.stringify(c.proxy.monitor));
        // A new push token each time
        delete m.pushToken;
        return sort(m);
    });

/**
 * Fill each visible field once, in order, with a value of its own
 * @returns {Promise<number>} How many were filled
 */
async function fillAll() {
    const handles = await page
        .locator('form:has([data-testid="save-button"])')
        .locator("input, select, textarea")
        .elementHandles();
    let filled = 0;
    const seen = {};
    for (const h of handles) {
        const info = await h.evaluate((el) => ({
            visible: !!(el.offsetWidth || el.offsetHeight || el.getClientRects().length),
            tag: el.tagName.toLowerCase(),
            type: el.type,
            readOnly: el.readOnly || el.disabled,
            // What the field is, whatever its place, so that moving it doesn't change its value
            key:
                el.id ||
                ((el.closest("form") ?? document).querySelector(`label[for="${CSS.escape(el.id)}"]`)?.textContent ??
                    "") ||
                el.closest("label")?.textContent ||
                el.getAttribute("aria-label") ||
                el.placeholder ||
                el.name ||
                "",
            testid: el.dataset.testid || "",
            multiselect: !!el.closest(".multiselect"),
        }));
        if (!info.visible || info.readOnly || info.multiselect || info.testid === "monitor-type-select") {
            continue;
        }
        // A number of the field's own, from its key, and its place among fields with the same key
        const key = info.key.replace(/\s+/g, " ").trim();
        seen[key] = (seen[key] ?? 0) + 1;
        let hash = 0;
        for (const ch of `${key}#${seen[key]}`) {
            hash = (hash * 31 + ch.charCodeAt(0)) % 1000;
        }
        try {
            if (info.tag === "select") {
                const values = await h.evaluate((el) => [...el.options].map((o) => o.value));
                if (values.length > 1) {
                    await h.selectOption(values[values.length - 1]);
                }
            } else if (info.type === "checkbox" || info.type === "radio") {
                await h.click({ timeout: 2000 });
            } else if (info.type === "number") {
                await h.fill(String(20 + (hash % 50)));
            } else if (info.type === "url") {
                await h.fill(`http://h${hash}.test/`);
            } else if (info.type === "color" || info.type === "file" || info.type === "range") {
                continue;
            } else {
                await h.fill(`v${hash}`);
            }
            filled++;
        } catch {
            // A field that a dialog or another field covers
        }
    }
    return filled;
}

const snapshot = {};
for (const type of types) {
    await page.goto("/add");
    await page.getByTestId("monitor-type-select").selectOption(type);
    await page.waitForTimeout(400);
    // The folded Advanced section, where the form has one
    const advanced = page.getByTestId("advanced-toggle");
    if ((await advanced.count()) > 0 && (await advanced.getAttribute("aria-expanded")) === "false") {
        await advanced.click();
        await page.waitForTimeout(200);
    }
    const before = await fields();
    const filled = await fillAll();
    await page.waitForTimeout(300);
    snapshot[type] = { fields: before, filled, monitor: await formMonitor() };
    process.stdout.write(`${type}:${before.length} `);
}

writeFileSync(out, JSON.stringify(snapshot, null, 1));
await browser.close();
console.log(`\n${types.length} types; page errors: ${errors.length}`);
for (const e of errors.slice(0, 5)) {
    console.log("page error:", e);
}
