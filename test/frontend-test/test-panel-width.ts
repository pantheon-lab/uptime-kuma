import { describe, test } from "node:test";
import assert from "node:assert";
import { clampListWidth, keyListWidth, maxListWidth, parseListWidth } from "../../src/panel-width";

describe("clampListWidth()", () => {
    test("between 280 pixels and what leaves the page 360, at most 70%", () => {
        assert.strictEqual(clampListWidth(100, 1400), 280);
        assert.strictEqual(clampListWidth(500.4, 1400), 500);
        assert.strictEqual(clampListWidth(1200, 1400), 980);
        assert.strictEqual(clampListWidth(900, 1000), 640);
    });

    test("a row too narrow for both keeps the list's minimum", () => {
        assert.strictEqual(maxListWidth(500), 280);
        assert.strictEqual(clampListWidth(400, 500), 280);
    });
});

describe("parseListWidth()", () => {
    test("a stored width, or none", () => {
        assert.strictEqual(parseListWidth("420"), 420);
        assert.strictEqual(parseListWidth(""), null);
        assert.strictEqual(parseListWidth("abc"), null);
        assert.strictEqual(parseListWidth("-5"), null);
        assert.strictEqual(parseListWidth(null), null);
    });
});

describe("keyListWidth()", () => {
    test("arrows step, Shift steps further, Home and End go to the ends, Enter resets", () => {
        assert.strictEqual(keyListWidth("ArrowRight", false, 400, 1400), 424);
        assert.strictEqual(keyListWidth("ArrowLeft", true, 400, 1400), 304);
        assert.strictEqual(keyListWidth("ArrowLeft", true, 300, 1400), 280);
        assert.strictEqual(keyListWidth("Home", false, 400, 1400), 280);
        assert.strictEqual(keyListWidth("End", false, 400, 1400), 980);
        assert.strictEqual(keyListWidth("Enter", false, 400, 1400), null);
        assert.strictEqual(keyListWidth("a", false, 400, 1400), undefined);
    });
});
