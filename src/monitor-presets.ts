/**
 * Presets for how often a monitor checks and how soon it alerts, offered by the Edit Monitor form.
 */

export interface MonitorPreset {
    /** Key, also the suffix of its translation keys */
    key: "critical" | "standard" | "medium" | "relaxed";
    /** Seconds between checks */
    interval: number;
    /** Retries before the monitor is down */
    maxretries: number;
    /** Seconds between retries */
    retryInterval: number;
    /** Seconds before a check times out, where the type has a request timeout */
    timeout: number;
    /** Failed checks between repeated notifications, 0 for none */
    resendInterval: number;
}

// Every interval is at least 20 seconds, so that a preset never asks to confirm a low interval,
// and every timeout is within 80% of its interval, which the form enforces.
export const MONITOR_PRESETS: readonly MonitorPreset[] = [
    { key: "critical", interval: 30, maxretries: 2, retryInterval: 20, timeout: 15, resendInterval: 5 },
    { key: "standard", interval: 60, maxretries: 1, retryInterval: 30, timeout: 30, resendInterval: 5 },
    { key: "medium", interval: 120, maxretries: 2, retryInterval: 60, timeout: 48, resendInterval: 3 },
    { key: "relaxed", interval: 300, maxretries: 3, retryInterval: 60, timeout: 48, resendInterval: 2 },
];

// Types whose timeout is a request timeout that a preset sets. Ping's is a global timeout tied to
// its count, and Kafka, RabbitMQ and SNMP have short defaults of their own, so theirs stay.
const PRESET_TIMEOUT_TYPES = new Set(["http", "keyword", "json-query", "websocket-upgrade"]);

/**
 * Whether a preset sets the timeout of a monitor type
 * @param type Monitor type
 * @returns Whether it does
 */
export function presetSetsTimeout(type: string): boolean {
    return PRESET_TIMEOUT_TYPES.has(type);
}

interface PresetFields {
    type: string;
    interval: number | string;
    maxretries: number | string;
    retryInterval: number | string;
    timeout?: number | string | null;
    resendInterval: number | string;
}

/**
 * The preset a monitor's settings match, if any. A form's inputs may hold strings.
 * @param monitor Monitor as the form has it
 * @returns Key of the matching preset, or null
 */
export function matchingPreset(monitor: PresetFields): MonitorPreset["key"] | null {
    const preset = MONITOR_PRESETS.find(
        (p) =>
            Number(monitor.interval) === p.interval &&
            Number(monitor.maxretries) === p.maxretries &&
            Number(monitor.retryInterval) === p.retryInterval &&
            (!presetSetsTimeout(monitor.type) || Number(monitor.timeout) === p.timeout) &&
            Number(monitor.resendInterval) === p.resendInterval
    );
    return preset?.key ?? null;
}
