import { describe, test } from "node:test";
import assert from "node:assert";
import { beatShape, MAINTENANCE_DARK, PENDING_EDGE_LIGHT, UP_EDGE_LIGHT } from "../../src/beat-shape";

/**
 * WCAG contrast ratio of two colours
 * @param {string} a Colour
 * @param {string} b Colour
 * @returns {number} Ratio
 */
function contrast(a: string, b: string): number {
    const lum = (hex: string) => {
        const [r, g, b] = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255);
        const f = (c: number) => (c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4);
        return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
    };
    const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x);
    return (hi + 0.05) / (lo + 0.05);
}

describe("beatShape()", () => {
    test("each status has a shape of its own", () => {
        const shapes = [0, 1, 2, 3].map((s) => beatShape(s, false));
        const keys = shapes.map((s) => `${s.height}/${s.notch}/${s.hollow}`);
        assert.strictEqual(new Set(keys).size, 4, keys.join(" "));
        assert.strictEqual(beatShape(0, false).notch, true);
        assert.strictEqual(beatShape(2, false).height, 0.6);
        assert.strictEqual(beatShape(3, true).hollow, true);
    });

    test("pale fills get an edge on light backgrounds only", () => {
        assert.strictEqual(beatShape(1, false).edge, UP_EDGE_LIGHT);
        assert.strictEqual(beatShape(2, false).edge, PENDING_EDGE_LIGHT);
        assert.strictEqual(beatShape(1, true).edge, null);
        assert.strictEqual(beatShape(null, false).edge, null);
    });

    test("the colours meet 3:1 against their backgrounds", () => {
        assert.ok(contrast(UP_EDGE_LIGHT, "#ffffff") >= 3);
        assert.ok(contrast(PENDING_EDGE_LIGHT, "#ffffff") >= 3);
        assert.ok(contrast(MAINTENANCE_DARK, "#0d1117") >= 3);
        assert.strictEqual(beatShape(3, true).fill, MAINTENANCE_DARK);
    });
});
