import { describe, test } from "node:test";
import assert from "node:assert";
import {
    compareMonitors,
    dropPosition,
    moveInOrder,
    parseSortState,
    type SortableMonitor,
    type SortState,
} from "../../src/monitor-sort";
import { DOWN, MAINTENANCE, PENDING, UP } from "../../src/util";

const monitors: SortableMonitor[] = [
    { id: 1, name: "Beta", active: true, sortOrder: 2 },
    { id: 2, name: "alpha", active: true, sortOrder: 0 },
    { id: 3, name: "Gamma", active: false, sortOrder: 1 },
    { id: 4, name: "Delta 10", active: true, sortOrder: 0 },
    { id: 5, name: "Delta 9", active: true, sortOrder: 3 },
];
const status: Record<number, number> = { 1: UP, 2: DOWN, 3: DOWN, 4: MAINTENANCE };
const uptime: Record<number, number> = { 1: 0.99, 2: 0.5, 4: 1 };
const ctx = { status: (id: number) => status[id], uptime: (id: number) => uptime[id] };
const order = (sort: SortState) => [...monitors].sort((a, b) => compareMonitors(a, b, sort, ctx)).map((m) => m.id);

describe("compareMonitors()", () => {
    test("by name, numbers in order and case ignored, paused last", () => {
        assert.deepStrictEqual(order({ key: "name", desc: false }), [2, 1, 5, 4, 3]);
        assert.deepStrictEqual(order({ key: "name", desc: true }), [4, 5, 1, 2, 3]);
    });

    test("by status, down first, no beat last; reversed, up first", () => {
        assert.deepStrictEqual(order({ key: "status", desc: false }), [2, 4, 1, 5, 3]);
        assert.deepStrictEqual(order({ key: "status", desc: true }), [5, 1, 4, 2, 3]);
    });

    test("by uptime, worst first, unknown last either way", () => {
        assert.deepStrictEqual(order({ key: "uptime", desc: false }), [2, 1, 4, 5, 3]);
        assert.deepStrictEqual(order({ key: "uptime", desc: true }), [4, 1, 2, 5, 3]);
    });

    test("recently added first; reversed, oldest first", () => {
        assert.deepStrictEqual(order({ key: "added", desc: false }), [5, 4, 2, 1, 3]);
        assert.deepStrictEqual(order({ key: "added", desc: true }), [1, 2, 4, 5, 3]);
    });

    test("custom order as set, paused ones too, unordered ones after by name", () => {
        assert.deepStrictEqual(order({ key: "custom", desc: false }), [3, 1, 5, 2, 4]);
    });

    test("pending sorts between down and maintenance", () => {
        status[5] = PENDING;
        assert.deepStrictEqual(order({ key: "status", desc: false }), [2, 5, 4, 1, 3]);
        delete status[5];
    });
});

describe("moveInOrder()", () => {
    test("before and after a monitor", () => {
        assert.deepStrictEqual(moveInOrder([1, 2, 3, 4], 4, 2, "before"), [1, 4, 2, 3]);
        assert.deepStrictEqual(moveInOrder([1, 2, 3, 4], 1, 3, "after"), [2, 3, 1, 4]);
        assert.deepStrictEqual(moveInOrder([1, 2, 3, 4], 1, 4, "after"), [2, 3, 4, 1]);
    });

    test("nothing moves onto itself, to where it is, or outside the group", () => {
        assert.strictEqual(moveInOrder([1, 2, 3], 2, 2, "before"), null);
        assert.strictEqual(moveInOrder([1, 2, 3], 2, 3, "before"), null);
        assert.strictEqual(moveInOrder([1, 2, 3], 2, 1, "after"), null);
        assert.strictEqual(moveInOrder([1, 2, 3], 9, 1, "before"), null);
    });
});

describe("dropPosition()", () => {
    test("a monitor's halves; a group's edges, and its middle takes the monitor in", () => {
        assert.strictEqual(dropPosition(5, 40, false), "before");
        assert.strictEqual(dropPosition(30, 40, false), "after");
        assert.strictEqual(dropPosition(5, 40, true), "before");
        assert.strictEqual(dropPosition(20, 40, true), "into");
        assert.strictEqual(dropPosition(35, 40, true), "after");
    });
});

describe("parseSortState()", () => {
    test("what was stored, or the default", () => {
        assert.deepStrictEqual(parseSortState('{"key":"status","desc":true}'), { key: "status", desc: true });
        assert.deepStrictEqual(parseSortState('{"key":"weird"}'), { key: "name", desc: false });
        assert.deepStrictEqual(parseSortState("not json"), { key: "name", desc: false });
        assert.deepStrictEqual(parseSortState(null), { key: "name", desc: false });
    });
});
