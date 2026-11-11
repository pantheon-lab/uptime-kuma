import { describe, test } from "node:test";
import assert from "node:assert";
import { monitorPath, type PathMonitor } from "../../src/monitor-path";

const list = (...monitors: PathMonitor[]) => Object.fromEntries(monitors.map((m) => [String(m.id), m]));

describe("monitorPath()", () => {
    test("the groups above a monitor, top first", () => {
        const l = list(
            { id: 1, name: "Shop", parent: null },
            { id: 2, name: "EU", parent: 1 },
            { id: 3, name: "API", parent: 2 }
        );
        assert.strictEqual(monitorPath(l, l["3"]), "Shop / EU / API");
        assert.strictEqual(monitorPath(l, l["1"]), "Shop");
    });

    test("a group renamed or moved shows as it is now", () => {
        const l = list(
            { id: 1, name: "Store", parent: null },
            { id: 4, name: "Asia", parent: 1 },
            { id: 2, name: "EU", parent: 4 },
            { id: 3, name: "API", parent: 2 }
        );
        assert.strictEqual(monitorPath(l, l["3"]), "Store / Asia / EU / API");
    });

    test("a missing group ends the path", () => {
        const l = list({ id: 3, name: "API", parent: 9 });
        assert.strictEqual(monitorPath(l, l["3"]), "API");
    });

    test("groups that contain each other end the path instead of looping", () => {
        const l = list(
            { id: 1, name: "A", parent: 2 },
            { id: 2, name: "B", parent: 1 },
            { id: 3, name: "C", parent: 1 }
        );
        assert.strictEqual(monitorPath(l, l["3"]), "B / A / C");
        assert.strictEqual(monitorPath(l, l["1"]), "B / A");
    });
});
