const fs = require("fs");
const path = require("path");
const serverUrl = require("../../playwright.config.js").url;

const setupDoneMarker = "./../../data/playwright-test/e2e-setup-done";

/**
 * @param {TestInfo} testInfo Test info
 * @param {Page} page Page
 * @returns {Promise<void>}
 */
export async function screenshot(testInfo, page) {
    const screenshot = await page.screenshot();
    await testInfo.attach("screenshot", {
        body: screenshot,
        contentType: "image/png",
    });
}

/**
 * @param {Page} page Page
 * @returns {Promise<void>}
 */
export async function login(page) {
    // Login
    await page.getByPlaceholder("Username").click();
    await page.getByPlaceholder("Username").fill("admin");
    await page.getByPlaceholder("Username").press("Tab");
    await page.getByPlaceholder("Password").fill("Kuma-E2E-8f4Q2xR9p");
    await page.getByLabel("Remember me").check();
    await page.getByRole("button", { name: "Log in" }).click();
    await page.isVisible("text=Add New Monitor");
}

/**
 * Determines if the setup spec has finished, which it records by taking the first snapshot.
 * @returns {boolean} True if setup has completed
 */
export function getSetupCompleted() {
    return fs.existsSync(path.resolve(__dirname, setupDoneMarker));
}

/**
 * Makes a request to the server to take a snapshot of the SQLite database.
 * @param {Page|null} page Page
 * @returns {Promise<Response>} Promise of response from snapshot request.
 */
export async function takeSqliteSnapshot(page = null) {
    if (page) {
        return page.goto("./_e2e/take-sqlite-snapshot");
    } else {
        return fetch(`${serverUrl}/_e2e/take-sqlite-snapshot`);
    }
}

/**
 * Makes a request to the server to restore the snapshot of the SQLite database.
 * @returns {Promise<Response>} Promise of response from restoration request.
 */
export async function restoreSqliteSnapshot() {
    return fetch(`${serverUrl}/_e2e/restore-sqlite-snapshot`);
}
