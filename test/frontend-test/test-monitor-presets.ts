import { describe, test } from "node:test";
import assert from "node:assert";
import { MONITOR_PRESETS, matchingPreset, presetSetsTimeout } from "../../src/monitor-presets";

describe("monitor presets", () => {
    test("each stays within what the form allows", () => {
        for (const p of MONITOR_PRESETS) {
            assert.ok(p.interval >= 20, `${p.key} interval`);
            assert.ok(p.retryInterval >= 20, `${p.key} retry interval`);
            assert.ok(p.timeout <= ~~(p.interval * 8) / 10, `${p.key} timeout within 80% of the interval`);
            assert.ok(p.maxretries >= 0 && p.resendInterval >= 0, `${p.key} counts`);
        }
    });

    test("keys are unique", () => {
        assert.strictEqual(new Set(MONITOR_PRESETS.map((p) => p.key)).size, MONITOR_PRESETS.length);
    });

    test("a monitor with a preset's values matches it, even with strings from inputs", () => {
        for (const p of MONITOR_PRESETS) {
            assert.strictEqual(matchingPreset({ type: "http", ...p }), p.key);
            assert.strictEqual(
                matchingPreset({
                    type: "http",
                    interval: String(p.interval),
                    maxretries: String(p.maxretries),
                    retryInterval: String(p.retryInterval),
                    timeout: String(p.timeout),
                    resendInterval: String(p.resendInterval),
                }),
                p.key
            );
        }
    });

    test("a changed field leaves no preset", () => {
        const standard = MONITOR_PRESETS.find((p) => p.key === "standard")!;
        assert.strictEqual(matchingPreset({ type: "http", ...standard, maxretries: 5 }), null);
        assert.strictEqual(matchingPreset({ type: "http", ...standard, timeout: 12 }), null);
    });

    test("the timeout only counts for types a preset sets it on", () => {
        const standard = MONITOR_PRESETS.find((p) => p.key === "standard")!;
        assert.ok(presetSetsTimeout("http"));
        assert.ok(!presetSetsTimeout("ping"));
        assert.ok(!presetSetsTimeout("snmp"));
        assert.strictEqual(matchingPreset({ type: "snmp", ...standard, timeout: 5 }), "standard");
        assert.strictEqual(matchingPreset({ type: "ping", ...standard, timeout: 10 }), "standard");
    });

    test("the form's old defaults match none", () => {
        assert.strictEqual(
            matchingPreset({
                type: "http",
                interval: 60,
                maxretries: 0,
                retryInterval: 60,
                timeout: 48,
                resendInterval: 0,
            }),
            null
        );
    });
});
