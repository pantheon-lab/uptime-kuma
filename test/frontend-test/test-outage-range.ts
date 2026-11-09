import { describe, test } from "node:test";
import assert from "node:assert";
import { outageChartRange } from "../../src/outage-range";

const now = new Date("2026-11-08T12:00:00Z");

describe("outageChartRange()", () => {
    test("a quarter of the outage either side", () => {
        const r = outageChartRange({ start: "2026-11-08 00:00:00.000", end: "2026-11-08 04:00:00.000" }, now);
        assert.strictEqual(r.from.toISOString(), "2026-11-07T23:00:00.000Z");
        assert.strictEqual(r.to.toISOString(), "2026-11-08T05:00:00.000Z");
    });

    test("at least 15 minutes either side of a short one", () => {
        const r = outageChartRange({ start: "2026-11-08 10:00:00.000", end: "2026-11-08 10:02:00.000" }, now);
        assert.strictEqual(r.from.toISOString(), "2026-11-08T09:45:00.000Z");
        assert.strictEqual(r.to.toISOString(), "2026-11-08T10:17:00.000Z");
    });

    test("one going on runs to now, and none past it", () => {
        const r = outageChartRange({ start: "2026-11-08 11:00:00.000", end: null }, now);
        assert.strictEqual(r.from.toISOString(), "2026-11-08T10:45:00.000Z");
        assert.strictEqual(r.to.toISOString(), "2026-11-08T12:00:00.000Z");
    });
});
