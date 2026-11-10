import { describe, test } from "node:test";
import assert from "node:assert";
import { readFileSync } from "node:fs";
import { monitorSummary } from "../../src/monitor-summary";

// The English strings, interpolated as vue-i18n does for named values
const en = JSON.parse(readFileSync(new URL("../../src/lang/en.json", import.meta.url), "utf8"));

/**
 * @param {string} key Key
 * @param {object} values Values
 * @returns {string} English text
 */
function t(key, values = {}) {
    return en[key].replace(/\{(\w+)\}/g, (_, name) => String(values[name]));
}

/**
 * @param {number} s Seconds
 * @returns {string} Duration
 */
const duration = (s) => (s % 60 === 0 ? `${s / 60} min` : `${s} s`);

/**
 * @param {object} m Monitor
 * @param {string[]} n Notifications
 * @returns {object} Summary
 */
const summary = (m, n = []) => monitorSummary(m, n, t, duration);

describe("monitorSummary()", () => {
    test("an HTTP monitor with retries and alerts", () => {
        const s = summary(
            {
                type: "http",
                url: "https://example.com",
                method: "post",
                interval: 60,
                retryInterval: 30,
                maxretries: 2,
            },
            ["Slack", "Email"]
        );
        assert.strictEqual(
            s.text,
            "Requests POST https://example.com every 1 min, and goes down after 3 failed checks in a row, 30 s apart"
        );
        assert.strictEqual(s.alerts, "It alerts Slack, Email.");
        assert.strictEqual(s.silent, false);
    });

    test("by type", () => {
        assert.strictEqual(
            summary({ type: "keyword", url: "https://x", keyword: "ok", interval: 20 }).text,
            "Requests GET https://x and expects “ok” in the response every 20 s, and goes down at the first failed check"
        );
        assert.ok(
            summary({ type: "keyword", url: "u", keyword: "err", invertKeyword: true }).text.includes(
                "expects no “err”"
            )
        );
        assert.ok(
            summary({
                type: "json-query",
                url: "u",
                jsonPath: "$.status",
                jsonPathOperator: "==",
                expectedValue: "up",
            }).text.includes("expects $.status == up")
        );
        assert.ok(
            summary({ type: "port", hostname: "db", port: 5432 }).text.startsWith("Connects to db on port 5432 every")
        );
        assert.ok(
            summary({ type: "dns", hostname: "example.com", dns_resolve_type: "MX" }).text.startsWith(
                "Looks up the MX record of example.com"
            )
        );
        assert.ok(summary({ type: "postgres" }).text.startsWith("Connects to the PostgreSQL database"));
        assert.ok(summary({ type: "snmp", hostname: "switch1" }).text.startsWith("Checks switch1 (snmp)"));
        assert.ok(summary({ type: "steam" }).text.startsWith("Runs a steam check"));
        assert.strictEqual(
            summary({ type: "push", interval: 300 }).text,
            "Expects a push at least every 5 min, and goes down when one doesn't come"
        );
        assert.strictEqual(summary({ type: "group" }).text, "Is down when a monitor in it is down");
        assert.strictEqual(summary({ type: "manual" }).text, "Is up or down as you set it, without checking anything");
    });

    test("what isn't filled in yet shows as …", () => {
        assert.ok(summary({ type: "http", url: "" }).text.startsWith("Requests GET … every"));
        assert.ok(summary({ type: "port", hostname: " " }).text.startsWith("Connects to … on port …"));
    });

    test("no alerts, and upside down mode", () => {
        const s = summary({ type: "ping", hostname: "h", upsideDown: true });
        assert.strictEqual(s.silent, true);
        assert.strictEqual(s.alerts, "It alerts no one: choose notifications under Notifications.");
        assert.deepStrictEqual(s.notes, [
            "Upside down mode is on: it's up when the check fails, and down when it succeeds.",
        ]);
        assert.deepStrictEqual(summary({ type: "group", upsideDown: true }).notes, []);
    });
});
