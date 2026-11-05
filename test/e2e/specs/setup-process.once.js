import { test } from "../fixtures";
import { getSetupCompleted, login, screenshot, takeSqliteSnapshot } from "../util-test";

test.describe("Uptime Kuma Setup", () => {
    test.skip(() => getSetupCompleted(), "Must only run once per session");

    test.afterEach(async ({ page }, testInfo) => {
        await screenshot(testInfo, page);
    });

    /*
     * Setup
     */

    test("setup admin", async ({ page }) => {
        await page.goto("./setup");
        await page.getByRole("textbox", { name: "Username" }).click();
        await page.getByRole("textbox", { name: "Username" }).fill("admin");
        await page.getByRole("textbox", { name: "Password", exact: true }).fill("Kuma-E2E-8f4Q2xR9p");
        await page.getByRole("textbox", { name: "Repeat Password" }).fill("Kuma-E2E-8f4Q2xR9p");
        await page.getByRole("button", { name: "Create" }).click();
        // User is auto-logged in and redirected to dashboard
        await page.waitForURL("/dashboard");
    });

    /*
     * All other tests should be run after setup
     */

    test("login", async ({ page }) => {
        await page.goto("./dashboard");
        await login(page);
    });

    test("failed login shows error alert", async ({ page }, testInfo) => {
        await page.goto("./dashboard");
        await page.getByPlaceholder("Username").fill("admin");
        await page.getByPlaceholder("Password").fill("wrongpassword");
        await page.getByRole("button", { name: "Log in" }).click();
        await page.waitForSelector(".alert.alert-danger", { state: "visible" });
        await screenshot(testInfo, page);
    });

    test("logout", async ({ page }) => {
        await page.goto("./dashboard");
        await login(page);
        await page.getByText("A", { exact: true }).click();
        await page.getByRole("button", { name: "Log out" }).click();
    });

    test("take sqlite snapshot", async ({ page }) => {
        await takeSqliteSnapshot(page);
    });
});
