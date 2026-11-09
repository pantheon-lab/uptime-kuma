import { describe, test } from "node:test";
import assert from "node:assert";
import { smtpNotifications, startsIn, subscriptionLink, upcomingWhen } from "../../src/status-subscriptions";

const token = "AbCdEfGhIjKlMnOpQrStUvWxYz012345";

describe("subscriptionLink()", () => {
    test("reads a confirmation or unsubscribe link", () => {
        assert.deepStrictEqual(subscriptionLink({ subscription: "confirm", token }), { action: "confirm", token });
        assert.deepStrictEqual(subscriptionLink({ subscription: "unsubscribe", token }), {
            action: "unsubscribe",
            token,
        });
    });

    test("ignores other queries", () => {
        assert.strictEqual(subscriptionLink({}), null);
        assert.strictEqual(subscriptionLink({ edit: null }), null);
        assert.strictEqual(subscriptionLink({ subscription: "delete", token }), null);
        assert.strictEqual(subscriptionLink({ subscription: "confirm" }), null);
        assert.strictEqual(subscriptionLink({ subscription: "confirm", token: "short" }), null);
        assert.strictEqual(subscriptionLink({ subscription: "confirm", token: token + "<script>" }), null);
        assert.strictEqual(subscriptionLink({ subscription: "confirm", token: [token] }), null);
    });
});

describe("upcomingWhen()", () => {
    test("one date when the window ends the same day, in the given time zone", () => {
        const text = upcomingWhen("2026-11-14T18:00:00Z", "2026-11-14T20:00:00Z", "en-GB", "Asia/Hong_Kong");
        assert.match(text, /Sun 15 Nov/);
        assert.match(text, /02:00/);
        assert.match(text, /04:00/);
        assert.strictEqual(text.match(/Nov/g)?.length, 1);
    });

    test("both dates when it runs past midnight", () => {
        const text = upcomingWhen("2026-11-14T22:00:00Z", "2026-11-15T02:00:00Z", "en-GB", "UTC");
        assert.match(text, /14 Nov/);
        assert.match(text, /15 Nov/);
    });
});

describe("startsIn()", () => {
    const now = new Date("2026-11-09T00:00:00Z");
    test("minutes, then hours, then days", () => {
        assert.strictEqual(startsIn("2026-11-09T00:20:00Z", now, "en"), "in 20 minutes");
        assert.strictEqual(startsIn("2026-11-09T03:00:00Z", now, "en"), "in 3 hours");
        assert.strictEqual(startsIn("2026-11-10T12:00:00Z", now, "en"), "in 36 hours");
        assert.strictEqual(startsIn("2026-11-12T00:00:00Z", now, "en"), "in 3 days");
    });

    test("never in the past or now", () => {
        assert.strictEqual(startsIn("2026-11-08T00:00:00Z", now, "en"), "in 1 minute");
    });
});

describe("smtpNotifications()", () => {
    test("the email notifications, by name, from their config", () => {
        const list = [
            { id: 1, name: "Slack", config: JSON.stringify({ type: "slack" }) },
            { id: 2, name: "Ops mail", config: JSON.stringify({ type: "smtp" }) },
            { id: 3, name: "Broken", config: "{" },
            { id: 4, name: "Alerts mail", config: JSON.stringify({ type: "smtp" }) },
            { id: 5, name: "No config" },
        ];
        assert.deepStrictEqual(
            smtpNotifications(list).map((n) => n.id),
            [4, 2]
        );
    });
});
