// The dashboard's first-run guide: add a monitor, get alerted, share a status page. It starts only
// when this browser sees a server with no monitors, so that existing installations never get it,
// and stays until its steps are done or it's hidden.

export type GuideState = "" | "active" | "hidden" | "done";

export interface GuideCounts {
    monitors: number;
    notifications: number;
    statusPages: number;
}

export interface GuideStep {
    key: "monitor" | "notification" | "statusPage";
    done: boolean;
}

export const GUIDE_KEY = "gettingStarted";

/**
 * The steps, and which are done
 * @param counts What the server has
 * @returns Steps, in order
 */
export function guideSteps(counts: GuideCounts): GuideStep[] {
    return [
        { key: "monitor", done: counts.monitors > 0 },
        { key: "notification", done: counts.notifications > 0 },
        { key: "statusPage", done: counts.statusPages > 0 },
    ];
}

/**
 * The guide's next state, from the saved one and what the server has
 * @param saved The state saved in this browser
 * @param counts What the server has
 * @returns The state to save, and show the guide while it's "active"
 */
export function nextGuideState(saved: GuideState, counts: GuideCounts): GuideState {
    if (saved === "hidden" || saved === "done") {
        return saved;
    }
    if (guideSteps(counts).every((s) => s.done)) {
        // Only an active guide finishes: a server set up before the guide never started it
        return saved === "active" ? "done" : saved;
    }
    if (saved === "active" || counts.monitors === 0) {
        return "active";
    }
    return "";
}

/**
 * Read the saved state
 * @param storage Where it's kept, such as localStorage
 * @returns The state
 */
export function loadGuideState(storage: Pick<Storage, "getItem"> | undefined): GuideState {
    try {
        const v = storage?.getItem(GUIDE_KEY);
        return v === "active" || v === "hidden" || v === "done" ? v : "";
    } catch {
        return "";
    }
}
