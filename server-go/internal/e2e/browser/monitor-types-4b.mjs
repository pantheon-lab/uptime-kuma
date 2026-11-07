// Creates one monitor of each Phase 4b type through the web UI's form, then reads its first beat
// through the REST API, and checks that editing shows the saved settings.
//
// Usage: node monitor-types-4b.mjs <playwright-core dir> <chromium> <base URL> <admin token> <screenshot dir>
//
// It needs a new database, and the test servers that server-go/README.md starts, on this machine's
// ports, so the server must use the host's network. The MQTT case publishes with docker exec.
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

/**
 * Add a condition to the form
 * @param {string} operator Operator id
 * @param {string} value Value
 * @returns {Promise<void>}
 */
async function addCondition(operator, value) {
    await page.getByTestId("add-condition-button").click();
    await page.getByTestId("condition-operator").last().selectOption(operator);
    await page.getByTestId("condition-value").last().fill(value);
}

const cases = [
    {
        type: "sqlserver",
        name: "SQL Server",
        fill: async () => {
            await page
                .locator("#connectionString")
                .fill("Server=127.0.0.1,51433;User Id=sa;Password=Kuma-Test-Pw1!;TrustServerCertificate=true");
            await page.locator("#sqlQuery").fill("SELECT name FROM sys.databases");
        },
        want: (b) => b.status === 1 && /^Rows: \d+$/.test(b.msg),
        reopen: async () => (await page.locator("#sqlQuery").inputValue()) === "SELECT name FROM sys.databases",
    },
    {
        type: "oracledb",
        name: "Oracle with a condition",
        fill: async () => {
            await page.locator("#connectionString").fill("127.0.0.1:51521/FREEPDB1");
            await page.locator("#oracledb-user").fill("kuma");
            await page.locator("#oracledb-pass input, input#oracledb-pass").first().fill("kumatest");
            await page.locator("#sqlQuery").fill("SELECT 'open' FROM DUAL");
            await addCondition("equals", "open");
        },
        want: (b) => b.status === 1 && b.msg === "Query did meet specified conditions",
        reopen: async () => (await page.locator("#oracledb-user").inputValue()) === "kuma",
    },
    {
        type: "mongodb",
        name: "MongoDB",
        fill: async () => {
            await page.locator("#connectionString").fill("mongodb://127.0.0.1:57017");
            await page.locator("#sqlQuery, textarea#sqlQuery, #mongodbCommand").first().fill('{"serverStatus": 1}');
            await page.locator("#jsonPath").fill("ok");
            await page.locator("#expectedValue").fill("1");
        },
        want: (b) => b.status === 1 && b.msg === "Command executed successfully and expected value was found",
        reopen: async () => (await page.locator("#jsonPath").inputValue()) === "ok",
    },
    {
        type: "mqtt",
        name: "MQTT",
        before: async () => {
            const { execFileSync } = await import("node:child_process");
            execFileSync("docker", [
                "exec",
                "kuma-go-test-mqtt",
                "mosquitto_pub",
                "-t",
                "kuma/browser",
                "-r",
                "-m",
                "status: running",
            ]);
        },
        fill: async () => {
            // The form asks for a scheme in this field.
            await page.locator("#hostname").fill("mqtt://127.0.0.1");
            await page.locator("#port").fill("51883");
            await page.locator("#mqttTopic").fill("kuma/browser");
            await page.locator("#mqttSuccessKeyword").fill("running");
        },
        want: (b) => b.status === 1 && b.msg === "Topic: kuma/browser; Message: status: running",
        reopen: async () => (await page.locator("#mqttTopic").inputValue()) === "kuma/browser",
    },
    {
        type: "rabbitmq",
        name: "RabbitMQ",
        fill: async () => {
            // The multiselect's input is hidden until its box is clicked.
            await page
                .locator("#rabbitmqNodes")
                .locator("xpath=ancestor::div[contains(@class, 'multiselect')][1]")
                .click();
            await page.keyboard.type("http://127.0.0.1:55672");
            await page.keyboard.press("Enter");
            await page.locator("#rabbitmqUsername").fill("guest");
            await page.locator("#rabbitmqPassword input, input#rabbitmqPassword").first().fill("guest");
        },
        want: (b) => b.status === 1 && b.msg === "Node is reachable and there are no alerts in the cluster",
        reopen: async () => (await page.locator("#rabbitmqUsername").inputValue()) === "guest",
    },
    {
        type: "kafka-producer",
        name: "Kafka",
        fill: async () => {
            // The multiselect's input is hidden until its box is clicked.
            await page
                .locator("#kafkaProducerBrokers")
                .locator("xpath=ancestor::div[contains(@class, 'multiselect')][1]")
                .click();
            await page.keyboard.type("127.0.0.1:59092");
            await page.keyboard.press("Enter");
            await page.locator("#kafkaProducerTopic").fill("uptime-kuma-browser");
            await page.locator("#kafkaProducerMessage").fill("hello from the form");
            await page.locator("#kafkaProducerAllowAutoTopicCreation").check();
        },
        want: (b) => b.status === 1 && b.msg === "Message sent successfully",
        reopen: async () => (await page.locator("#kafkaProducerTopic").inputValue()) === "uptime-kuma-browser",
    },
];

let failed = 0;
for (const c of cases) {
    try {
        if (c.before) {
            await c.before();
        }
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
