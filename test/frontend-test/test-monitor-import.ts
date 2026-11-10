import { describe, test } from "node:test";
import assert from "node:assert";
import { load } from "js-yaml";
import { csvToMonitors, detectFormat, parseCSV, yamlToMonitors } from "../../src/monitor-import";

describe("parseCSV()", () => {
    test("quotes, commas, line breaks and blank lines", () => {
        const rows = parseCSV('﻿a,b\r\n"x, y","say ""hi""\nthere"\n\n1,\n');
        assert.deepStrictEqual(rows, [
            ["a", "b"],
            ["x, y", 'say "hi"\nthere'],
            ["1", ""],
        ]);
    });
});

describe("csvToMonitors()", () => {
    test("typed fields, and empty cells left out", () => {
        const csv = [
            "name,type,url,interval,upsideDown,acceptedStatusCodes,headers,hostname",
            'api,http,https://api.example.com,30,yes,200-299;301,"{""X-Key"":""1""}",',
            "db,port,,,,,,db.internal",
        ].join("\n");
        const { monitors, errors } = csvToMonitors(csv);
        assert.deepStrictEqual(errors, []);
        assert.deepStrictEqual(monitors, [
            {
                name: "api",
                type: "http",
                url: "https://api.example.com",
                interval: 30,
                upsideDown: true,
                acceptedStatusCodes: ["200-299", "301"],
                headers: { "X-Key": "1" },
            },
            { name: "db", type: "port", hostname: "db.internal" },
        ]);
    });

    test("cells of the wrong type name their row", () => {
        const { errors } = csvToMonitors("name,interval,active,headers\na,soon,maybe,{bad\n");
        assert.deepStrictEqual(errors, [
            'Row 1: interval must be a number, not "soon".',
            'Row 1: active must be true or false, not "maybe".',
            "Row 1: headers must be JSON.",
        ]);
    });

    test("a header that isn't usable", () => {
        assert.match(csvToMonitors("name\n").errors[0], /header row/);
        assert.match(csvToMonitors("name,name\na,b\n").errors[0], /twice/);
        assert.match(csvToMonitors("name,\na,b\n").errors[0], /no name/);
        assert.match(csvToMonitors("name\na,b\n").errors[0], /more cells/);
    });
});

describe("yamlToMonitors()", () => {
    test("a list, or a map with a monitors list, and JSON", () => {
        const list = "- name: api\n  type: http\n  url: https://api.example.com\n  interval: 30\n";
        assert.deepStrictEqual(yamlToMonitors(list, load).monitors, [
            { name: "api", type: "http", url: "https://api.example.com", interval: 30 },
        ]);
        assert.strictEqual(yamlToMonitors("monitors:\n  - name: a\n  - name: b\n", load).monitors.length, 2);
        assert.strictEqual(yamlToMonitors('[{"name": "a"}]', load).monitors[0].name, "a");
    });

    test("what isn't a list of monitors", () => {
        assert.match(yamlToMonitors("name: a\n", load).errors[0], /list of monitors/);
        assert.match(yamlToMonitors("- a\n- name: b\n", load).errors[0], /Row 1/);
        assert.match(yamlToMonitors("- name: [\n", load).errors[0], /isn't valid YAML/);
    });
});

describe("detectFormat()", () => {
    test("from the name, then the contents", () => {
        assert.strictEqual(detectFormat("m.CSV", "- a"), "csv");
        assert.strictEqual(detectFormat("m.yml", "name,type"), "yaml");
        assert.strictEqual(detectFormat("", "  - name: a"), "yaml");
        assert.strictEqual(detectFormat("", "monitors:\n"), "yaml");
        assert.strictEqual(detectFormat("", "name,type\n"), "csv");
    });
});
