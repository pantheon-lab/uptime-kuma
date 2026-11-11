import { describe, test } from "node:test";
import assert from "node:assert";
import { previewBeats } from "../../src/appearance-preview";
import { DOWN, MAINTENANCE, PENDING, UP } from "../../src/util";

describe("previewBeats()", () => {
    const now = new Date(Date.UTC(2026, 10, 10, 9, 30, 15, 250));

    test("one a minute, oldest first, ending now, in UTC", () => {
        const beats = previewBeats(now, 3, {}, "refused");
        assert.deepStrictEqual(
            beats.map((b) => b.time),
            ["2026-11-10 09:28:15.250", "2026-11-10 09:29:15.250", "2026-11-10 09:30:15.250"]
        );
        assert.ok(beats.every((b) => b.status === UP && b.msg === "" && typeof b.ping === "number"));
    });

    test("statuses by minutes ago; only down beats have the message, only up ones a ping", () => {
        const beats = previewBeats(now, 4, { 0: DOWN, 1: PENDING, 3: MAINTENANCE }, "refused");
        assert.deepStrictEqual(
            beats.map((b) => [b.status, b.msg, b.ping === null]),
            [
                [MAINTENANCE, "", true],
                [UP, "", false],
                [PENDING, "", true],
                [DOWN, "refused", true],
            ]
        );
    });
});
