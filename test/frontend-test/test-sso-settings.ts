import { describe, test } from "node:test";
import assert from "node:assert";
import { callbackURL, parseTeams, teamsText } from "../../src/sso-settings";

describe("parseTeams()", () => {
    test("lowercase slugs, by commas or spaces, without repeats or blanks", () => {
        assert.deepStrictEqual(parseTeams(" SRE, support ,,sre  platform-team "), ["sre", "support", "platform-team"]);
        assert.deepStrictEqual(parseTeams(""), []);
        assert.deepStrictEqual(parseTeams("*"), ["*"]);
    });

    test("round-trips with teamsText()", () => {
        assert.deepStrictEqual(parseTeams(teamsText(["sre", "support"])), ["sre", "support"]);
    });
});

describe("callbackURL()", () => {
    test("the server's when it has a base URL", () => {
        assert.deepStrictEqual(callbackURL("https://kuma.example/api/auth/github/callback", "http://localhost:3000"), {
            url: "https://kuma.example/api/auth/github/callback",
            guessed: false,
        });
    });

    test("else this page's, marked as a guess", () => {
        assert.deepStrictEqual(callbackURL("", "https://kuma.example/"), {
            url: "https://kuma.example/api/auth/github/callback",
            guessed: true,
        });
    });
});
