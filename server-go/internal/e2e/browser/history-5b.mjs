// Phase 5b through the web UI: the retention setting, the database size, clearing a monitor's
// beats and every monitor's statistics, search engine visibility, and the settings that are
// gone from Settings > General.
//
// Usage: node history-5b.mjs <playwright-core dir> <chromium> <base URL> <screenshot dir> <psql command>
//
// It needs a new database. The psql command runs SQL in it, read from standard input, such as
// "docker exec -i kuma-go-test psql -U postgres -d e2e5b -tA".
import { execSync } from "node:child_process";
import { createRequire } from "node:module";
import path from "node:path";

const [pwDir, chromiumPath, baseURL, shots, psqlCommand] = process.argv.slice(2);
const require = createRequire(path.join(pwDir, "index.js"));
const { chromium } = require(pwDir);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const password = "Kuma-E2E-5b-x8Q2rT7p";

/**
 * Run SQL in the server's database
 * @param {string} sql Statements
 * @returns {string} Output, trimmed
 */
const sql = (sql) => execSync(psqlCommand, { input: sql, encoding: "utf8" }).trim();

/**
 * The heartbeat retention policy's days, or "none"
 * @returns {string} Days
 */
const retentionPolicy = () =>
    sql(
        "SELECT COALESCE((SELECT extract(day FROM (config->>'drop_after')::interval)::text FROM timescaledb_information.jobs " +
            "WHERE proc_name = 'policy_retention' AND hypertable_name = 'heartbeat'), 'none')"
    );

const browser = await chromium.launch({ executablePath: chromiumPath });
const context = await browser.newContext({ baseURL, viewport: { width: 1400, height: 1100 }, timezoneId: "UTC" });
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
 * Wait until a check passes
 * @param {Function} check Returns true when done
 * @param {string} what For the error
 * @returns {Promise<void>}
 */
async function until(check, what) {
    for (let i = 0; i < 60; i++) {
        if (await check()) {
            return;
        }
        await sleep(500);
    }
    throw new Error(`timed out waiting for ${what}`);
}

await step("setup and log in", async () => {
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

await step("the retention shows the days that apply", async () => {
    await page.goto("/settings/monitor-history");
    const field = page.locator("#keepDataPeriodDays");
    await until(async () => (await field.inputValue()) === "365", "365 days");
    if (retentionPolicy() !== "365") {
        throw new Error(`policy ${retentionPolicy()}`);
    }
    const size = await page.locator("#database-size").innerText();
    if (!/Database size: \d+(\.\d)? MB/.test(size) || / 0 MB/.test(size)) {
        throw new Error(size);
    }
    if (await page.getByRole("button", { name: /Shrink Database/ }).count()) {
        throw new Error("the shrink button is shown");
    }
    return size;
});

await step("saving the retention changes the policy", async () => {
    await page.locator("#keepDataPeriodDays").fill("30");
    await page.getByRole("button", { name: "Save" }).click();
    await page.waitForSelector("text=Saved");
    await until(() => retentionPolicy() === "30", "a 30 day policy");
    await page.locator("#keepDataPeriodDays").fill("0");
    await page.getByRole("button", { name: "Save" }).click();
    await until(() => retentionPolicy() === "none", "no policy");
    await page.locator("#keepDataPeriodDays").fill("45");
    await page.getByRole("button", { name: "Save" }).click();
    await until(() => retentionPolicy() === "45", "a 45 day policy");
    return "30, forever, 45";
});

let monitorID;
await step("clearing a monitor's beats clears its statistics", async () => {
    await page.goto("/add");
    await page.locator("#type").selectOption("http");
    await page.locator("#name").fill("Self");
    await page.locator("#url").fill(`${baseURL}/robots.txt`);
    await page.locator("#interval").fill("20");
    await page.getByRole("button", { name: "Save" }).click();
    await page.waitForURL(/\/dashboard\/\d+$/);
    monitorID = Number(page.url().split("/").pop());
    await until(() => Number(sql(`SELECT count(*) FROM heartbeat WHERE monitor_id = ${monitorID}`)) > 0, "a beat");
    // History from before the beats kept, as retention leaves it.
    sql(`INSERT INTO heartbeat (monitor_id, time, status, ping)
         SELECT ${monitorID}, now() - d * INTERVAL '1 day', 1, 10 FROM generate_series(2, 20) d;
         CALL refresh_continuous_aggregate('stat_hourly', NULL, date_trunc('hour', now()));
         CALL refresh_continuous_aggregate('stat_daily', NULL, date_trunc('day', now()));`);
    const before = sql(`SELECT count(*) FROM stat_daily WHERE monitor_id = ${monitorID}`);
    await page.getByRole("button", { name: "Clear Data" }).click();
    await page.getByRole("button", { name: "Heartbeats" }).click();
    await page.locator(".modal.show").getByRole("button", { name: "Yes" }).click();
    await until(
        () =>
            Number(
                sql(
                    `SELECT count(*) FROM stat_daily WHERE monitor_id = ${monitorID} AND bucket < now() - INTERVAL '1 day'`
                )
            ) === 0,
        "the old statistics to go"
    );
    // The monitor is started again, and goes on.
    await until(() => Number(sql(`SELECT count(*) FROM heartbeat WHERE monitor_id = ${monitorID}`)) > 0, "a new beat");
    return `${before} days of statistics before, none after, and checking goes on`;
});

await step("clearing all statistics", async () => {
    await page.goto("/settings/monitor-history");
    await page.locator("#clearAllStats-btn").click();
    await page.locator(".modal.show").getByRole("button", { name: "Yes" }).click();
    await page.waitForSelector("#keepDataPeriodDays");
    const left = sql(`SELECT (SELECT count(*) FROM stat_hourly WHERE bucket < date_trunc('hour', now())) +
                             (SELECT count(*) FROM heartbeat WHERE time < now() - INTERVAL '1 minute')`);
    if (left !== "0") {
        throw new Error(`${left} rows left`);
    }
    if ((await page.locator("#keepDataPeriodDays").inputValue()) !== "45") {
        throw new Error("the retention changed");
    }
});

await step("search engine visibility decides robots.txt", async () => {
    const robots = async () => (await (await page.request.get("/robots.txt")).text()).trim();
    if ((await robots()) !== "User-agent: *\nDisallow: /") {
        throw new Error(`by default: ${await robots()}`);
    }
    await page.goto("/settings/general");
    await page.locator("#searchEngineIndexYes").check();
    await page.getByRole("button", { name: "Save" }).click();
    await page.waitForSelector("text=Saved");
    if ((await robots()) !== "User-agent: *\nDisallow:") {
        throw new Error(`allowed: ${await robots()}`);
    }
});

await step("General has only settings that take effect", async () => {
    await page.goto("/settings/general");
    await page.waitForSelector("#searchEngineIndexYes");
    for (const id of ["nscdEnable", "prometheusApiEnabled", "apiEnabled"]) {
        if (await page.locator(`#${id}`).count()) {
            throw new Error(`#${id} is shown`);
        }
    }
    if (!(await page.locator("#serverTimezone").isDisabled())) {
        throw new Error("the server timezone can be changed");
    }
    await page.getByText("KUMA_TIMEZONE").waitFor();
    await page.screenshot({ path: path.join(shots, "general.png"), fullPage: true });
});

await page.goto("/settings/monitor-history");
await sleep(1000);
await page.screenshot({ path: path.join(shots, "monitor-history.png"), fullPage: true });
await browser.close();
const passed = results.filter(Boolean).length;
console.log(`${passed}/${results.length} passed; page errors: ${errors.length}`);
for (const e of errors.slice(0, 5)) {
    console.log("page error:", e);
}
process.exit(passed === results.length ? 0 : 1);
