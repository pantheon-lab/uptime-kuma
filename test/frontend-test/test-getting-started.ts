import { describe, test } from "node:test";
import assert from "node:assert";
import { guideSteps, loadGuideState, nextGuideState } from "../../src/getting-started";

const none = { monitors: 0, notifications: 0, statusPages: 0 };

describe("nextGuideState()", () => {
    test("starts on a server with no monitors, and stays while steps are left", () => {
        assert.strictEqual(nextGuideState("", none), "active");
        assert.strictEqual(nextGuideState("active", { ...none, monitors: 3 }), "active");
    });

    test("never starts on a server that already has monitors", () => {
        assert.strictEqual(nextGuideState("", { ...none, monitors: 3 }), "");
        assert.strictEqual(nextGuideState("", { monitors: 3, notifications: 1, statusPages: 1 }), "");
    });

    test("finishes when every step is done, and hiding it lasts", () => {
        const all = { monitors: 1, notifications: 1, statusPages: 1 };
        assert.strictEqual(nextGuideState("active", all), "done");
        assert.strictEqual(nextGuideState("done", none), "done");
        assert.strictEqual(nextGuideState("hidden", none), "hidden");
    });
});

describe("guideSteps()", () => {
    test("each step from its count", () => {
        assert.deepStrictEqual(
            guideSteps({ monitors: 2, notifications: 0, statusPages: 1 }).map((s) => [s.key, s.done]),
            [
                ["monitor", true],
                ["notification", false],
                ["statusPage", true],
            ]
        );
    });
});

describe("loadGuideState()", () => {
    test("known values only, and storage that throws", () => {
        assert.strictEqual(loadGuideState({ getItem: () => "hidden" }), "hidden");
        assert.strictEqual(loadGuideState({ getItem: () => "nonsense" }), "");
        assert.strictEqual(
            loadGuideState({
                getItem: () => {
                    throw new Error("blocked");
                },
            }),
            ""
        );
        assert.strictEqual(loadGuideState(undefined), "");
    });
});
