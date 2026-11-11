import { describe, test } from "node:test";
import assert from "node:assert";
import { dump, load } from "js-yaml";
import { exportFileName, monitorsToFile } from "../../src/monitor-export";
import { detectFormat, yamlToMonitors } from "../../src/monitor-import";

const monitors = [
    { id: 3, name: "Shop", type: "group", parentId: null, active: true },
    {
        id: 7,
        name: "API: v2 # main",
        type: "http",
        parentId: 3,
        url: "https://api.example.com",
        acceptedStatusCodes: ["200-299"],
        headers: '{"X-Key": "1"}',
        basicAuthPass: "yes",
    },
];

describe("monitorsToFile()", () => {
    for (const format of ["yaml", "json"] as const) {
        test(`${format} reads back as the same monitors`, () => {
            const text = monitorsToFile(monitors, format, (v) => dump(v, { noRefs: true, lineWidth: -1 }));
            const name = exportFileName(format, new Date(2026, 10, 9));
            assert.strictEqual(detectFormat(name, text), "yaml");
            assert.strictEqual(detectFormat("", text), "yaml");
            const parsed = yamlToMonitors(text, (t) => load(t));
            assert.deepStrictEqual(parsed.errors, []);
            assert.deepStrictEqual(parsed.monitors, monitors);
        });
    }
});

describe("exportFileName()", () => {
    test("the local day, padded", () => {
        assert.strictEqual(exportFileName("yaml", new Date(2026, 0, 5, 23, 30)), "monitors-2026-01-05.yaml");
        assert.strictEqual(exportFileName("json", new Date(2026, 10, 10)), "monitors-2026-11-10.json");
    });
});
