/**
 * The browser tab's title for each page of the app.
 *
 * Status pages set their own title from their settings, so they're left alone, as is "/",
 * which may show a status page.
 */

type Translate = (key: string) => string;

interface MonitorNames {
    [id: string]: { name?: string } | undefined;
}

export interface PageTitleInput {
    path: string;
    /** Whether the login form is shown instead of the page */
    login: boolean;
    /** Monitors down, shown first so that a background tab shows an outage */
    down: number;
    monitorList: MonitorNames;
}

// The i18n keys of the settings pages, as Settings.vue lists them
export const SETTINGS_PAGES: Record<string, string> = {
    general: "General",
    appearance: "Appearance",
    notifications: "Notifications",
    "reverse-proxy": "Reverse Proxy",
    tags: "Tags",
    holidays: "Holidays",
    "monitor-history": "Monitor History",
    "docker-hosts": "Docker Hosts",
    "remote-browsers": "Remote Browsers",
    security: "Security",
    "api-keys": "API Keys",
    accounts: "Accounts",
    proxies: "Proxies",
    about: "About",
};

/**
 * Whether the app sets the title on this path
 * @param {string} path Route path
 * @returns {boolean} False for status pages and "/"
 */
export function ownsTitle(path: string): boolean {
    return path !== "/" && !/^\/status(-page)?(\/|$)/.test(path);
}

/**
 * The parts of a page's name, most specific first
 * @param {string} path Route path
 * @param {Translate} t Translator
 * @param {MonitorNames} monitorList Monitors by id
 * @returns {string[]} Parts, empty when the page has no name of its own
 */
function pageParts(path: string, t: Translate, monitorList: MonitorNames): string[] {
    const monitorName = (id: string) => monitorList[id]?.name;
    let m: RegExpMatchArray | null;

    if ((m = path.match(/^\/dashboard\/(\d+)\/?$/))) {
        const name = monitorName(m[1]);
        return name ? [name] : [t("Dashboard")];
    }
    if ((m = path.match(/^\/edit\/(\d+)\/?$/))) {
        const name = monitorName(m[1]);
        return name ? [name, t("Edit")] : [t("Edit")];
    }
    if ((m = path.match(/^\/clone\/(\d+)\/?$/))) {
        const name = monitorName(m[1]);
        return name ? [name, t("Clone Monitor")] : [t("Clone Monitor")];
    }
    if ((m = path.match(/^\/settings\/([^/]+)/))) {
        const key = SETTINGS_PAGES[m[1]];
        return key ? [t(key), t("Settings")] : [t("Settings")];
    }
    if (/^\/maintenance\/edit\//.test(path)) {
        return [t("Edit Maintenance")];
    }
    if (/^\/maintenance\/clone\//.test(path)) {
        return [t("Clone Maintenance")];
    }

    const fixed: Record<string, string> = {
        "/dashboard": "Dashboard",
        "/list": "Dashboard",
        "/add": "Add New Monitor",
        "/settings": "Settings",
        "/manage-status-page": "Status Pages",
        "/add-status-page": "Add New Status Page",
        "/maintenance": "Maintenance",
        "/add-maintenance": "Schedule Maintenance",
        "/setup": "Create your admin account",
    };
    const key = fixed[path.replace(/\/$/, "") || "/"];
    return key ? [t(key)] : [];
}

/**
 * The tab's title, such as "(2) api-gateway - Uptime Kuma"
 * @param {PageTitleInput} input The page and the app's state
 * @param {Translate} t Translator
 * @param {string} appName The app's name
 * @returns {string|null} The title, or null where the page sets its own
 */
export function pageTitle(input: PageTitleInput, t: Translate, appName: string): string | null {
    if (!ownsTitle(input.path)) {
        return null;
    }
    const parts = input.login ? [t("Login")] : pageParts(input.path, t, input.monitorList);
    const title = [...parts, appName].join(" - ");
    return !input.login && input.down > 0 ? `(${input.down}) ${title}` : title;
}
