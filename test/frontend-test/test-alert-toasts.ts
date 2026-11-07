import { describe, test } from "node:test";
import assert from "node:assert";
import { isMuted, planToasts } from "../../src/alert-toasts";

/**
 * Translator stub that shows the key and its values
 * @param {string} key Translation key
 * @param {object} values Values
 * @returns {string} Text
 */
function t(key, values) {
    return values ? `${key} ${JSON.stringify(values)}` : key;
}

const monitorList = {
    1: { name: "api", parent: null, tags: [] },
    2: { name: "db", parent: 10, tags: [{ tag_id: 7 }] },
    3: { name: "web", parent: 10, tags: [] },
    4: { name: "cdn", parent: null, tags: [] },
    5: { name: "dns", parent: null, tags: [] },
    10: { name: "backend", parent: null, tags: [] },
};
const none = { monitors: [], tags: [] };
const beat = (monitorID, status, msg = "msg") => ({ monitorID, status, msg });

describe("planToasts()", () => {
    test("a beat alone keeps the old pop-up", () => {
        assert.deepStrictEqual(planToasts([beat(1, 0, "refused")], monitorList, none, t), [
            { kind: "error", text: "[api] [DOWN] refused", monitorIDs: [1] },
        ]);
        assert.deepStrictEqual(planToasts([beat(1, 1, "200 - OK")], monitorList, none, t), [
            { kind: "success", text: "[api] [Up] 200 - OK", monitorIDs: [1] },
        ]);
    });

    test("beats together are grouped by status, down first", () => {
        const plans = planToasts(
            [beat(4, 1), beat(1, 0), beat(2, 0), beat(3, 0), beat(5, 0), beat(10, 0), beat(4, 2, "pending")],
            monitorList,
            none,
            t
        );
        assert.strictEqual(plans.length, 2);
        assert.strictEqual(plans[0].kind, "error");
        assert.deepStrictEqual(plans[0].monitorIDs, [1, 2, 3, 5, 10]);
        assert.strictEqual(
            plans[0].text,
            'toastMonitorsDown {"count":5,"names":"toastAndMore {\\"names\\":\\"api, db, web\\",\\"count\\":2}"}'
        );
        // cdn went up, then pending: only its latest counts
        assert.deepStrictEqual(plans[1], { kind: "info", text: "[cdn] pending", monitorIDs: [4] });
    });

    test("a short group lists every name", () => {
        const plans = planToasts([beat(1, 1), beat(4, 1)], monitorList, none, t);
        assert.deepStrictEqual(plans, [
            { kind: "success", text: 'toastMonitorsUp {"count":2,"names":"api, cdn"}', monitorIDs: [1, 4] },
        ]);
    });

    test("unknown and muted monitors show nothing", () => {
        const muted = { monitors: [10], tags: [] };
        assert.deepStrictEqual(planToasts([beat(99, 0), beat(2, 0), beat(3, 0)], monitorList, muted, t), []);
    });
});

describe("isMuted()", () => {
    test("by the monitor, a group it's in, or a tag", () => {
        assert.strictEqual(isMuted(1, monitorList, { monitors: [1], tags: [] }), true);
        assert.strictEqual(isMuted(3, monitorList, { monitors: [10], tags: [] }), true);
        assert.strictEqual(isMuted(2, monitorList, { monitors: [], tags: [7] }), true);
        assert.strictEqual(isMuted(3, monitorList, { monitors: [], tags: [7] }), false);
        assert.strictEqual(isMuted(10, monitorList, { monitors: [3], tags: [] }), false);
    });

    test("a loop of parents ends", () => {
        const loop = { 1: { parent: 2 }, 2: { parent: 1 } };
        assert.strictEqual(isMuted(1, loop, { monitors: [3], tags: [] }), false);
    });
});
