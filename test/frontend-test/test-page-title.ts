import { describe, test } from "node:test";
import assert from "node:assert";
import { ownsTitle, pageTitle } from "../../src/page-title";

/**
 * Translator stub that returns the key
 * @param {string} key Translation key
 * @returns {string} Key
 */
function t(key) {
    return key;
}

const monitorList = { 7: { name: "api-gateway" } };

/**
 * The title for a path with nothing down
 * @param {string} path Route path
 * @returns {string|null} Title
 */
function titleOf(path) {
    return pageTitle({ path, login: false, down: 0, monitorList }, t, "Uptime Kuma");
}

describe("pageTitle()", () => {
    test("names each page", () => {
        assert.strictEqual(titleOf("/dashboard"), "Dashboard - Uptime Kuma");
        assert.strictEqual(titleOf("/dashboard/7"), "api-gateway - Uptime Kuma");
        assert.strictEqual(titleOf("/edit/7"), "api-gateway - Edit - Uptime Kuma");
        assert.strictEqual(titleOf("/clone/7"), "api-gateway - Clone Monitor - Uptime Kuma");
        assert.strictEqual(titleOf("/add"), "Add New Monitor - Uptime Kuma");
        assert.strictEqual(titleOf("/settings/reverse-proxy"), "Reverse Proxy - Settings - Uptime Kuma");
        assert.strictEqual(titleOf("/maintenance/edit/3"), "Edit Maintenance - Uptime Kuma");
        assert.strictEqual(titleOf("/manage-status-page"), "Status Pages - Uptime Kuma");
    });

    test("falls back when the monitor isn't loaded yet", () => {
        assert.strictEqual(titleOf("/dashboard/99"), "Dashboard - Uptime Kuma");
        assert.strictEqual(titleOf("/edit/99"), "Edit - Uptime Kuma");
    });

    test("an unknown page has the app's name", () => {
        assert.strictEqual(titleOf("/nowhere"), "Uptime Kuma");
        assert.strictEqual(titleOf("/settings/nowhere"), "Settings - Uptime Kuma");
    });

    test("shows the monitors down first", () => {
        const title = pageTitle({ path: "/dashboard/7", login: false, down: 2, monitorList }, t, "Uptime Kuma");
        assert.strictEqual(title, "(2) api-gateway - Uptime Kuma");
    });

    test("the login form hides the page and the count", () => {
        const title = pageTitle({ path: "/dashboard/7", login: true, down: 2, monitorList }, t, "Uptime Kuma");
        assert.strictEqual(title, "Login - Uptime Kuma");
    });

    test("leaves status pages and the entry page alone", () => {
        for (const path of ["/", "/status", "/status/main", "/status-page"]) {
            assert.strictEqual(ownsTitle(path), false, path);
            assert.strictEqual(titleOf(path), null, path);
        }
        assert.strictEqual(ownsTitle("/statuses"), true);
    });
});
