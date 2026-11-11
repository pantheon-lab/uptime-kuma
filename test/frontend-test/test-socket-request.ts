import { describe, test } from "node:test";
import assert from "node:assert";
import { request } from "../../src/socket-request";

describe("request()", () => {
    test("sends the event with its arguments, and gives back the answer", async () => {
        let sent: unknown[] = [];
        const answer = await request(
            (...args: unknown[]) => {
                sent = args.slice(0, -1);
                (args[args.length - 1] as (res: unknown) => void)({ ok: false, msg: "no" });
            },
            "setMonitorOrder",
            [null, [3, 1]]
        );
        assert.deepStrictEqual(sent, ["setMonitorOrder", null, [3, 1]]);
        assert.deepStrictEqual(answer, { kind: "answered", res: { ok: false, msg: "no" } });
    });

    test("no answer in time is unknown, not a refusal, and a late one changes nothing", async () => {
        let late: (res: unknown) => void = () => {};
        const answer = await request(
            (...args: unknown[]) => (late = args[args.length - 1] as typeof late),
            "e",
            [],
            20
        );
        assert.deepStrictEqual(answer, { kind: "unknown" });
        assert.doesNotThrow(() => late({ ok: true }));
    });
});
