import { describe, test } from "node:test";
import assert from "node:assert";
import { setMonitorsActive } from "../../src/bulk-active";

type Ack = (res: unknown) => void;

describe("setMonitorsActive()", () => {
    test("sends the ids as numbers, and reports how many changed", async () => {
        let sent: unknown[] = [];
        const result = await setMonitorsActive(
            (event, ids, active, ack: Ack) => {
                sent = [event, ids, active];
                ack({ ok: true, count: 2 });
            },
            ["3", 7],
            true
        );
        assert.deepStrictEqual(sent, ["setMonitorsActive", [3, 7], true]);
        assert.deepStrictEqual(result, { kind: "done", count: 2 });
    });

    test("a refusal carries the server's message, or none", async () => {
        assert.deepStrictEqual(
            await setMonitorsActive(
                (_e, _i, _a, ack: Ack) => ack({ ok: false, msg: "no monitors were chosen" }),
                [1],
                false
            ),
            { kind: "failed", message: "no monitors were chosen" }
        );
        assert.deepStrictEqual(await setMonitorsActive((_e, _i, _a, ack: Ack) => ack(undefined), [1], false), {
            kind: "failed",
            message: null,
        });
    });

    test("no answer in time is unknown, and a late answer changes nothing", async () => {
        let late: Ack = () => {};
        const result = await setMonitorsActive((_e, _i, _a, ack: Ack) => (late = ack), [1], true, 20);
        assert.deepStrictEqual(result, { kind: "unknown" });
        assert.doesNotThrow(() => late({ ok: true, count: 1 }));
    });
});
