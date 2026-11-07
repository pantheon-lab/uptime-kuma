/**
 * The pop-ups shown when monitors go down or come back up. Beats that arrive together are
 * grouped, so that an outage of twenty monitors shows one pop-up instead of twenty, and monitors
 * can be muted in this browser, one by one, by group or by tag.
 */

type Translate = (key: string, values?: Record<string, unknown>) => string;

export interface ImportantBeat {
    monitorID: number;
    status: number;
    msg: string;
}

export interface MonitorInfo {
    name?: string;
    parent?: number | null;
    tags?: { tag_id: number }[];
}

export interface Muted {
    monitors: number[];
    tags: number[];
}

export interface ToastPlan {
    kind: "error" | "success" | "info";
    text: string;
    /** The monitors it's about */
    monitorIDs: number[];
}

const DOWN = 0;
const UP = 1;
const MUTED_KEY = "toastMuted";
// Names listed in a grouped pop-up before "and N more"
const NAMES_SHOWN = 3;

/**
 * The monitors and tags muted in this browser
 * @returns {Muted} Muted ids
 */
export function loadMuted(): Muted {
    const muted: Muted = { monitors: [], tags: [] };
    try {
        const saved = JSON.parse(localStorage.getItem(MUTED_KEY) || "{}");
        for (const key of ["monitors", "tags"] as const) {
            if (Array.isArray(saved[key])) {
                muted[key] = saved[key].filter((id: unknown) => Number.isInteger(id));
            }
        }
    } catch {
        // Storage may be blocked; nothing is muted then
    }
    return muted;
}

/**
 * Remember the monitors and tags muted
 * @param {Muted} muted Muted ids
 * @returns {void}
 */
export function saveMuted(muted: Muted): void {
    try {
        localStorage.setItem(MUTED_KEY, JSON.stringify(muted));
    } catch {
        // Muting lasts until the page is reloaded then
    }
}

/**
 * Whether a monitor's pop-ups are muted: itself, a group it's in, or one of its tags
 * @param {number} monitorID Monitor
 * @param {Record<string, MonitorInfo>} monitorList Monitors by id
 * @param {Muted} muted Muted ids
 * @returns {boolean} Whether they are
 */
export function isMuted(monitorID: number, monitorList: Record<string, MonitorInfo>, muted: Muted): boolean {
    const seen = new Set<number>();
    let id: number | null | undefined = monitorID;
    while (id != null && !seen.has(id)) {
        seen.add(id);
        if (muted.monitors.includes(id)) {
            return true;
        }
        id = monitorList[id]?.parent;
    }
    const tags = monitorList[monitorID]?.tags ?? [];
    return tags.some((tag) => muted.tags.includes(tag.tag_id));
}

/**
 * "a, b, c and 2 more"
 * @param {string[]} names Names
 * @param {Translate} t Translator
 * @returns {string} The list
 */
function listNames(names: string[], t: Translate): string {
    if (names.length <= NAMES_SHOWN) {
        return names.join(", ");
    }
    return t("toastAndMore", { names: names.slice(0, NAMES_SHOWN).join(", "), count: names.length - NAMES_SHOWN });
}

/**
 * The pop-ups for beats that arrived together: one per beat when it's alone in its status, one
 * per status otherwise, down first. Beats of unknown or muted monitors show none.
 * @param {ImportantBeat[]} beats Important beats, oldest first
 * @param {Record<string, MonitorInfo>} monitorList Monitors by id
 * @param {Muted} muted Muted ids
 * @param {Translate} t Translator
 * @returns {ToastPlan[]} Pop-ups
 */
export function planToasts(
    beats: ImportantBeat[],
    monitorList: Record<string, MonitorInfo>,
    muted: Muted,
    t: Translate
): ToastPlan[] {
    // A monitor that changed twice in the batch counts once, as its latest
    const latest = new Map<number, ImportantBeat>();
    for (const beat of beats) {
        if (monitorList[beat.monitorID] !== undefined && !isMuted(beat.monitorID, monitorList, muted)) {
            latest.delete(beat.monitorID);
            latest.set(beat.monitorID, beat);
        }
    }

    const byStatus = new Map<number, ImportantBeat[]>();
    for (const beat of latest.values()) {
        const key = beat.status === DOWN || beat.status === UP ? beat.status : -1;
        if (!byStatus.has(key)) {
            byStatus.set(key, []);
        }
        byStatus.get(key)!.push(beat);
    }

    const plans: ToastPlan[] = [];
    for (const status of [DOWN, UP, -1]) {
        const group = byStatus.get(status);
        if (!group) {
            continue;
        }
        const kind = status === DOWN ? "error" : status === UP ? "success" : "info";
        const name = (beat: ImportantBeat) => monitorList[beat.monitorID]?.name ?? String(beat.monitorID);
        if (status === -1 || group.length === 1) {
            // As before grouping: "[api] [DOWN] connection refused"
            for (const beat of group) {
                const label = status === DOWN ? " [DOWN]" : status === UP ? " [Up]" : "";
                plans.push({ kind, text: `[${name(beat)}]${label} ${beat.msg}`, monitorIDs: [beat.monitorID] });
            }
            continue;
        }
        const names = listNames(group.map(name), t);
        plans.push({
            kind,
            text: t(status === DOWN ? "toastMonitorsDown" : "toastMonitorsUp", { count: group.length, names }),
            monitorIDs: group.map((beat) => beat.monitorID),
        });
    }
    return plans;
}
