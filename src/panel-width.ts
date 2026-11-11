// The width of the dashboard's monitor list, which its edge resizes.

// Narrower, and the list's filters and names don't fit
export const MIN_LIST_WIDTH = 280;
// What the monitor's page beside it keeps at least
export const MIN_DETAIL_WIDTH = 360;
// Of the row, at most
export const MAX_LIST_SHARE = 0.7;
// The arrow keys' step, and with Shift
export const KEY_STEP = 24;
export const KEY_STEP_LARGE = 96;

/**
 * The widest the list can be beside the monitor's page
 * @param rowWidth The dashboard row's width
 * @returns The width, never under the minimum
 */
export function maxListWidth(rowWidth: number): number {
    return Math.max(MIN_LIST_WIDTH, Math.min(rowWidth * MAX_LIST_SHARE, rowWidth - MIN_DETAIL_WIDTH));
}

/**
 * A width kept between the minimum and the maximum, in whole pixels
 * @param width The width wanted
 * @param rowWidth The dashboard row's width
 * @returns The width used
 */
export function clampListWidth(width: number, rowWidth: number): number {
    return Math.round(Math.min(Math.max(width, MIN_LIST_WIDTH), maxListWidth(rowWidth)));
}

/**
 * A width read back from storage, or null for the layout's own
 * @param raw What was stored
 * @returns The width
 */
export function parseListWidth(raw: string | null | undefined): number | null {
    const n = Number(raw);
    return raw && Number.isFinite(n) && n > 0 ? n : null;
}

/**
 * The width after a key on the list's edge, or undefined for a key it doesn't use
 * @param key The key, as KeyboardEvent.key
 * @param shift Whether Shift is held, for bigger steps
 * @param width The width now
 * @param rowWidth The dashboard row's width
 * @returns The new width, null to go back to the layout's own, or undefined
 */
export function keyListWidth(key: string, shift: boolean, width: number, rowWidth: number): number | null | undefined {
    const step = shift ? KEY_STEP_LARGE : KEY_STEP;
    switch (key) {
        case "ArrowLeft":
            return clampListWidth(width - step, rowWidth);
        case "ArrowRight":
            return clampListWidth(width + step, rowWidth);
        case "Home":
            return MIN_LIST_WIDTH;
        case "End":
            return clampListWidth(maxListWidth(rowWidth), rowWidth);
        case "Enter":
            return null;
        default:
            return undefined;
    }
}
