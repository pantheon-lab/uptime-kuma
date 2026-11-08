/**
 * A sentence that says what a monitor will do, shown above the Edit Monitor form as it's filled
 * in, such as "Requests GET https://example.com every 60 seconds, and goes down after 3 failed
 * checks in a row. It alerts Slack."
 */

type Translate = (key: string, values?: Record<string, unknown>) => string;

export interface SummaryMonitor {
    type: string;
    url?: string | null;
    method?: string | null;
    hostname?: string | null;
    port?: number | string | null;
    keyword?: string | null;
    invertKeyword?: boolean;
    jsonPath?: string | null;
    jsonPathOperator?: string | null;
    expectedValue?: string | null;
    dns_resolve_type?: string | null;
    docker_container?: string | null;
    grpcUrl?: string | null;
    interval?: number;
    retryInterval?: number;
    maxretries?: number;
    upsideDown?: boolean;
}

export interface MonitorSummary {
    /** What it checks, and how often */
    text: string;
    /** Who it alerts, or that it alerts no one */
    alerts: string;
    /** Whether it alerts no one */
    silent: boolean;
    /** Things to look at, such as upside down mode */
    notes: string[];
}

const DATABASES: Record<string, string> = {
    sqlserver: "SQL Server",
    mongodb: "MongoDB",
    mysql: "MySQL",
    oracledb: "Oracle",
    postgres: "PostgreSQL",
    redis: "Redis",
};

/**
 * @param {string|null|undefined} text Text
 * @returns {string} The text trimmed, or "…" while it's empty
 */
function orBlank(text: string | number | null | undefined): string {
    const value = String(text ?? "").trim();
    return value === "" ? "…" : value;
}

/**
 * What the monitor checks
 * @param {SummaryMonitor} m Monitor
 * @param {Translate} t Translator
 * @returns {string} A phrase, such as "Requests GET https://example.com"
 */
function checks(m: SummaryMonitor, t: Translate): string {
    switch (m.type) {
        case "http":
        case "keyword":
        case "json-query":
        case "real-browser": {
            let text = t("summaryRequests", {
                method: m.type === "real-browser" ? "GET" : (m.method || "GET").toUpperCase(),
                url: orBlank(m.url),
            });
            if (m.type === "keyword") {
                text += t(m.invertKeyword ? "summaryKeywordAbsent" : "summaryKeyword", { keyword: orBlank(m.keyword) });
            } else if (m.type === "json-query") {
                text += t("summaryJsonQuery", {
                    query: orBlank(m.jsonPath),
                    operator: m.jsonPathOperator || "==",
                    expected: orBlank(m.expectedValue),
                });
            }
            return text;
        }
        case "websocket-upgrade":
            return t("summaryWebSocket", { url: orBlank(m.url) });
        case "port":
            return t("summaryPort", { host: orBlank(m.hostname), port: orBlank(m.port) });
        case "ping":
            return t("summaryPing", { host: orBlank(m.hostname) });
        case "dns":
            return t("summaryDns", { record: m.dns_resolve_type || "A", host: orBlank(m.hostname) });
        case "docker":
            return t("summaryDocker", { container: orBlank(m.docker_container) });
        case "grpc-keyword":
            return t("summaryGrpc", { url: orBlank(m.grpcUrl) }) + t("summaryKeyword", { keyword: orBlank(m.keyword) });
        default:
            if (DATABASES[m.type]) {
                return t("summaryDatabase", { database: DATABASES[m.type] });
            }
            if (m.hostname) {
                return t("summaryHost", { type: m.type, host: m.hostname });
            }
            return t("summaryOther", { type: m.type });
    }
}

/**
 * The summary of a monitor as the form has it
 * @param {SummaryMonitor} m Monitor
 * @param {string[]} notifications Names of the notifications it sends to
 * @param {Translate} t Translator
 * @param {Function} duration Formats seconds, such as "1 minute"
 * @returns {MonitorSummary} Summary
 */
export function monitorSummary(
    m: SummaryMonitor,
    notifications: string[],
    t: Translate,
    duration: (seconds: number) => string
): MonitorSummary {
    let text: string;
    const interval = duration(m.interval || 60);
    if (m.type === "manual") {
        text = t("summaryManual");
    } else if (m.type === "group") {
        text = t("summaryGroup");
    } else if (m.type === "push") {
        text = t("summaryPush", { interval });
    } else {
        const retries = m.maxretries ?? 0;
        const after =
            retries > 0
                ? t("summaryRetries", { count: retries + 1, retry: duration(m.retryInterval || m.interval || 60) })
                : t("summaryNoRetries");
        text = `${checks(m, t)} ${t("summaryEvery", { interval })}${after}`;
    }

    const notes: string[] = [];
    if (m.upsideDown && m.type !== "group" && m.type !== "manual") {
        notes.push(t("summaryUpsideDown"));
    }
    const silent = notifications.length === 0;
    return {
        text,
        alerts: silent ? t("summaryNoAlerts") : t("summaryAlerts", { names: notifications.join(", ") }),
        silent,
        notes,
    };
}
