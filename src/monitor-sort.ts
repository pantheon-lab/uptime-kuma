// How the monitor list is sorted, and the custom order that dragging sets.

import { DOWN, MAINTENANCE, PENDING, UP } from "./util";

export type SortKey = "custom" | "name" | "status" | "uptime" | "added";

export interface SortState {
    key: SortKey;
    // Reversed: Z to A, up first, best uptime first, oldest first
    desc: boolean;
}

export interface SortableMonitor {
    id: number;
    name: string;
    active: boolean;
    sortOrder?: number;
}

export interface SortContext {
    // The newest beat's status, if there is one
    status: (id: number) => number | undefined;
    // The last 24 hours' uptime, 0 to 1, if known
    uptime: (id: number) => number | undefined;
}

export const SORT_KEYS: SortKey[] = ["custom", "name", "status", "uptime", "added"];

export const DEFAULT_SORT: SortState = { key: "name", desc: false };

// Worst first: down, pending, maintenance, up, then no beat yet
const STATUS_RANK: Record<number, number> = { [DOWN]: 0, [PENDING]: 1, [MAINTENANCE]: 2, [UP]: 3 };
const NO_STATUS_RANK = 4;

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: "base" });

/**
 * A sort read back from storage, or the default when it isn't one
 * @param raw What was stored, as JSON
 * @returns The sort
 */
export function parseSortState(raw: string | null | undefined): SortState {
    try {
        const v = JSON.parse(raw ?? "");
        if (v && SORT_KEYS.includes(v.key)) {
            return { key: v.key, desc: v.desc === true };
        }
    } catch {
        // Not JSON: the default
    }
    return { ...DEFAULT_SORT };
}

/**
 * Compare two monitors of the same group for Array.sort. Paused monitors come last, except in the
 * custom order, which is as it was set. Ties go by name, then by id, so the order never jumps.
 * @param a A monitor
 * @param b Another
 * @param sort The sort
 * @param ctx Where status and uptime come from
 * @returns Negative when a comes first
 */
export function compareMonitors(a: SortableMonitor, b: SortableMonitor, sort: SortState, ctx: SortContext): number {
    const byName = () => collator.compare(a.name ?? "", b.name ?? "") || a.id - b.id;
    if (sort.key === "custom") {
        // 0 is unordered: after the ordered ones
        const ao = a.sortOrder && a.sortOrder > 0 ? a.sortOrder : Infinity;
        const bo = b.sortOrder && b.sortOrder > 0 ? b.sortOrder : Infinity;
        return ao !== bo ? (ao < bo ? -1 : 1) : byName();
    }
    if (a.active !== b.active) {
        return a.active ? -1 : 1;
    }
    let result = 0;
    switch (sort.key) {
        case "name":
            result = byName();
            break;
        case "status": {
            const rank = (id: number) => STATUS_RANK[ctx.status(id) as number] ?? NO_STATUS_RANK;
            result = rank(a.id) - rank(b.id);
            break;
        }
        case "uptime": {
            const ua = ctx.uptime(a.id);
            const ub = ctx.uptime(b.id);
            // Unknown uptime goes last, whichever way
            if (ua === undefined || ub === undefined) {
                if (ua !== ub) {
                    return ua === undefined ? 1 : -1;
                }
            } else {
                result = ua - ub;
            }
            break;
        }
        case "added":
            // Newest first, unless reversed
            result = b.id - a.id;
            break;
    }
    if (sort.desc && sort.key !== "name") {
        result = -result;
    } else if (sort.desc) {
        return -byName();
    }
    return result || byName();
}

/**
 * The ids of a group's monitors after dragging one before or after another
 * @param ids The group's monitors, in the order shown
 * @param dragged The monitor moved
 * @param target The monitor it was dropped on
 * @param position Before or after the target
 * @returns The new order, or null when nothing moves
 */
export function moveInOrder(
    ids: number[],
    dragged: number,
    target: number,
    position: "before" | "after"
): number[] | null {
    if (dragged === target || !ids.includes(dragged) || !ids.includes(target)) {
        return null;
    }
    const rest = ids.filter((id) => id !== dragged);
    const at = rest.indexOf(target) + (position === "after" ? 1 : 0);
    const moved = [...rest.slice(0, at), dragged, ...rest.slice(at)];
    return moved.every((id, i) => id === ids[i]) ? null : moved;
}

/**
 * Where a drag over a row would drop: before it, after it, or into it when it's a group
 * @param offsetY The pointer's distance from the row's top
 * @param height The row's height
 * @param isGroup Whether the row is a group, which takes monitors into it in its middle
 * @returns The place
 */
export function dropPosition(offsetY: number, height: number, isGroup: boolean): "before" | "after" | "into" {
    const part = height > 0 ? offsetY / height : 0.5;
    if (isGroup) {
        if (part < 0.25) {
            return "before";
        }
        return part > 0.75 ? "after" : "into";
    }
    return part < 0.5 ? "before" : "after";
}
