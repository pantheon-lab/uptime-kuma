/**
 * Keyboard shortcuts of the app, as the keys pressed and what they do.
 */

export type ShortcutAction =
    | "palette"
    | "help"
    | "search"
    | "newMonitor"
    | "next"
    | "previous"
    | "edit"
    | "goDashboard"
    | "goStatusPages"
    | "goMaintenance"
    | "goSettings";

export interface KeyInput {
    key: string;
    ctrlKey?: boolean;
    metaKey?: boolean;
    altKey?: boolean;
    /** Whether the key went to a field being typed in */
    editable?: boolean;
    /** When it was pressed, in milliseconds */
    time: number;
}

/** The shortcuts, for the list that "?" shows. Labels are i18n keys. */
export const SHORTCUTS: { keys: string[]; action: ShortcutAction; label: string }[] = [
    { keys: ["Ctrl", "K"], action: "palette", label: "shortcutPalette" },
    { keys: ["/"], action: "search", label: "shortcutSearch" },
    { keys: ["n"], action: "newMonitor", label: "shortcutNewMonitor" },
    { keys: ["j"], action: "next", label: "shortcutNext" },
    { keys: ["k"], action: "previous", label: "shortcutPrevious" },
    { keys: ["e"], action: "edit", label: "shortcutEdit" },
    { keys: ["g", "d"], action: "goDashboard", label: "shortcutGoDashboard" },
    { keys: ["g", "s"], action: "goStatusPages", label: "shortcutGoStatusPages" },
    { keys: ["g", "m"], action: "goMaintenance", label: "shortcutGoMaintenance" },
    { keys: ["g", ","], action: "goSettings", label: "shortcutGoSettings" },
    { keys: ["?"], action: "help", label: "shortcutHelp" },
];

const SINGLE: Record<string, ShortcutAction> = {
    "/": "search",
    n: "newMonitor",
    j: "next",
    k: "previous",
    e: "edit",
    "?": "help",
};

const AFTER_G: Record<string, ShortcutAction> = {
    d: "goDashboard",
    s: "goStatusPages",
    m: "goMaintenance",
    ",": "goSettings",
};

// How long after "g" its second key counts
const SEQUENCE_MS = 1500;

/**
 * Turns key presses into shortcuts, remembering a "g" for the key after it
 */
export class ShortcutMatcher {
    private gAt: number | null = null;

    /**
     * The shortcut a key press makes, if any
     * @param {KeyInput} input The key press
     * @returns {ShortcutAction|null} The shortcut
     */
    match(input: KeyInput): ShortcutAction | null {
        const key = input.key;
        if ((input.ctrlKey || input.metaKey) && !input.altKey && key.toLowerCase() === "k") {
            this.gAt = null;
            return "palette";
        }
        if (input.editable || input.ctrlKey || input.metaKey || input.altKey) {
            this.gAt = null;
            return null;
        }
        if (this.gAt !== null && input.time - this.gAt <= SEQUENCE_MS) {
            this.gAt = null;
            return AFTER_G[key] ?? null;
        }
        if (key === "g") {
            this.gAt = input.time;
            return null;
        }
        this.gAt = null;
        return SINGLE[key] ?? null;
    }
}
