import { describe, test, before, after } from "node:test";
import assert from "node:assert";
import { R } from "redbean-node";
import dayjs from "dayjs";
import utc from "dayjs/plugin/utc";
import TestDB from "../mock-testdb";
import { MonitorStats } from "../../server/monitor-stats";

dayjs.extend(utc);

// The application relies on these on every database, including TimescaleDB
describe("Database value types", () => {
    const testDb = new TestDB("./data/test-database-types");
    let monitorID: number;

    before(async () => {
        await testDb.create();
        const monitor = R.dispense("monitor");
        monitor.name = "types";
        monitor.type = "http";
        monitor.url = "http://example.com";
        monitorID = await R.store(monitor);
    });

    after(async () => {
        await testDb.destroy();
    });

    test("store returns the new id", () => {
        assert.ok(monitorID > 0);
    });

    test("booleans read back as 0/1 and match in SQL", async () => {
        const beat = R.dispense("heartbeat");
        beat.monitor_id = monitorID;
        beat.status = 1;
        beat.time = R.isoDateTimeMillis(dayjs.utc());
        beat.important = true;
        await R.store(beat);

        const row = await R.getRow("SELECT important FROM heartbeat WHERE id = ?", [beat.id]);
        assert.strictEqual(row.important, 1);
        assert.strictEqual(await R.count("heartbeat", "monitor_id = ? AND important = 1", [monitorID]), 1);
    });

    test("datetimes read back as strings in UTC", async () => {
        const at = "2026-01-02 03:04:05.678";
        const beat = R.dispense("heartbeat");
        beat.monitor_id = monitorID;
        beat.status = 1;
        beat.time = at;
        await R.store(beat);

        const time = await R.getCell("SELECT time FROM heartbeat WHERE id = ?", [beat.id]);
        assert.strictEqual(typeof time, "string");
        assert.strictEqual(dayjs.utc(time).valueOf(), dayjs.utc(at).valueOf());
    });

    test("backtick-quoted reserved words work in raw SQL", async () => {
        await R.exec("UPDATE `monitor` SET name = ? WHERE id = ?", ["it's `quoted`", monitorID]);
        assert.strictEqual(await R.getCell("SELECT `name` FROM `monitor` WHERE id = ?", [monitorID]), "it's `quoted`");
    });

    test("dashboard stats add up", async () => {
        const now = dayjs.utc().unix();
        const row = { monitor_id: monitorID, ping: 50, ping_min: 50, ping_max: 50, up: 3, down: 1 };
        await R.knex("stat_minutely").insert({ ...row, timestamp: Math.floor(now / 60) * 60 });
        await R.knex("stat_daily").insert({ ...row, timestamp: Math.floor(now / 86400) * 86400 });

        const stats = await MonitorStats.compute([monitorID]);
        assert.deepStrictEqual(stats[monitorID], { avgPing: 50, uptime24: 0.75, uptime720: 0.75, uptime1y: 0.75 });
    });
});
