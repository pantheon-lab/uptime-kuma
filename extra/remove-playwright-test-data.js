const fs = require("fs");

fs.rmSync("./data/playwright-test", {
    recursive: true,
    force: true,
});

// With E2E_DB_TYPE=postgres, the test data also lives in a database on the server
if (process.env.E2E_DB_TYPE === "postgres") {
    require("node:process").loadEnvFile();
    const { Client } = require("pg");
    const client = new Client({
        host: process.env.UPTIME_KUMA_DB_HOSTNAME,
        port: Number(process.env.UPTIME_KUMA_DB_PORT),
        user: process.env.UPTIME_KUMA_DB_USERNAME,
        password: process.env.UPTIME_KUMA_DB_PASSWORD,
        database: "postgres",
    });
    (async () => {
        await client.connect();
        await client.query("DROP DATABASE IF EXISTS kuma_playwright WITH (FORCE)");
        await client.query("DROP DATABASE IF EXISTS kuma_playwright_snapshot WITH (FORCE)");
        await client.end();
    })();
}
