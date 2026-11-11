import { describe, test } from "node:test";
import assert from "node:assert";
import { rowClickAction } from "../../src/row-click";

const click = (extra = {}) => ({
    button: 0,
    detail: 1,
    ctrlKey: false,
    metaKey: false,
    shiftKey: false,
    altKey: false,
    ...extra,
});

describe("rowClickAction()", () => {
    test("opens the monitor when nothing is ticked", () => {
        assert.strictEqual(rowClickAction(click(), false, true), "navigate");
    });

    test("ticks or unticks the row while monitors are being chosen", () => {
        assert.strictEqual(rowClickAction(click(), true, true), "toggle");
    });

    test("a row without a box, for a viewer, always opens", () => {
        assert.strictEqual(rowClickAction(click(), true, false), "navigate");
    });

    test("a new tab, a new window or another button is the browser's, even while choosing", () => {
        for (const extra of [
            { ctrlKey: true },
            { metaKey: true },
            { shiftKey: true },
            { altKey: true },
            { button: 1 },
        ]) {
            assert.strictEqual(rowClickAction(click(extra), true, true), "browser", JSON.stringify(extra));
            assert.strictEqual(rowClickAction(click(extra), false, true), "browser", JSON.stringify(extra));
        }
    });

    test("Enter on the focused row opens it, even while choosing", () => {
        assert.strictEqual(rowClickAction(click({ detail: 0 }), true, true), "navigate");
        assert.strictEqual(rowClickAction(click({ detail: 0 }), false, true), "navigate");
    });
});
