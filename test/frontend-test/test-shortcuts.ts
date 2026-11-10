import { describe, test } from "node:test";
import assert from "node:assert";
import { ShortcutMatcher } from "../../src/shortcuts";
import { matchScore, searchCommands } from "../../src/command-search";

describe("ShortcutMatcher", () => {
    test("single keys", () => {
        const m = new ShortcutMatcher();
        assert.strictEqual(m.match({ key: "/", time: 0 }), "search");
        assert.strictEqual(m.match({ key: "n", time: 0 }), "newMonitor");
        assert.strictEqual(m.match({ key: "j", time: 0 }), "next");
        assert.strictEqual(m.match({ key: "?", time: 0 }), "help");
        assert.strictEqual(m.match({ key: "x", time: 0 }), null);
    });

    test("g then a key, within a second and a half", () => {
        const m = new ShortcutMatcher();
        assert.strictEqual(m.match({ key: "g", time: 1000 }), null);
        assert.strictEqual(m.match({ key: "d", time: 2000 }), "goDashboard");
        assert.strictEqual(m.match({ key: "d", time: 2100 }), null);
        m.match({ key: "g", time: 3000 });
        assert.strictEqual(m.match({ key: "s", time: 5000 }), null, "too late");
        m.match({ key: "g", time: 6000 });
        assert.strictEqual(m.match({ key: "n", time: 6100 }), null, "g n isn't one, nor is n after it");
        assert.strictEqual(m.match({ key: "n", time: 6200 }), "newMonitor");
    });

    test("typing in a field, or with modifiers, isn't a shortcut, but Ctrl+K is", () => {
        const m = new ShortcutMatcher();
        assert.strictEqual(m.match({ key: "/", editable: true, time: 0 }), null);
        assert.strictEqual(m.match({ key: "n", ctrlKey: true, time: 0 }), null);
        assert.strictEqual(m.match({ key: "k", ctrlKey: true, editable: true, time: 0 }), "palette");
        assert.strictEqual(m.match({ key: "K", metaKey: true, time: 0 }), "palette");
        assert.strictEqual(m.match({ key: "k", ctrlKey: true, altKey: true, time: 0 }), null);
        m.match({ key: "g", time: 0 });
        assert.strictEqual(m.match({ key: "d", editable: true, time: 10 }), null, "typing ends a sequence");
        assert.strictEqual(m.match({ key: "d", time: 20 }), null);
    });
});

describe("searchCommands()", () => {
    const commands = [
        { id: "a", label: "Add New Monitor", group: "Action", to: "/add" },
        { id: "1", label: "api-gateway", group: "Monitor", to: "/dashboard/1" },
        { id: "2", label: "Backend / payments-api", group: "Monitor", to: "/dashboard/2" },
        { id: "3", label: "rapid", group: "Monitor", to: "/dashboard/3" },
        { id: "s", label: "Reverse Proxy", group: "Settings", to: "/settings/reverse-proxy", keywords: "nginx" },
    ];
    const ids = (q) => searchCommands(commands, q).map((c) => c.id);

    test("the start, then a word's start, then anywhere, then letters in order", () => {
        assert.deepStrictEqual(ids("api"), ["1", "2", "3"]);
        assert.deepStrictEqual(ids("pay"), ["2", "1"], "a word's start before letters in order");
        assert.deepStrictEqual(ids("agw"), ["1"]);
        assert.deepStrictEqual(ids("zzz"), []);
    });

    test("keywords find an entry", () => {
        assert.deepStrictEqual(ids("nginx"), ["s"]);
    });

    test("an empty query lists them in order, up to the limit", () => {
        assert.deepStrictEqual(ids("  "), ["a", "1", "2", "3", "s"]);
        assert.strictEqual(searchCommands(commands, "", 2).length, 2);
    });

    test("scores", () => {
        assert.ok(matchScore("api", "api") > matchScore("x api", "api"));
        assert.ok(matchScore("x api", "api") > matchScore("xapi", "api"));
        assert.ok(matchScore("xapi", "api") > matchScore("a-p-i", "api"));
        assert.strictEqual(matchScore("abc", "d"), -1);
    });
});
