// What a click on a monitor's row in the list does.

export type RowClickAction = "toggle" | "navigate" | "browser";

export interface RowClick {
    button: number;
    // How many clicks: 0 when the keyboard activated the link, with Enter
    detail: number;
    ctrlKey: boolean;
    metaKey: boolean;
    shiftKey: boolean;
    altKey: boolean;
}

/**
 * Tick or untick the row while monitors are being chosen, else open the monitor. A click the
 * browser handles itself, such as Ctrl or Cmd for a new tab, or another button, is left to it.
 * Enter on the focused row always opens it: the keyboard ticks with the row's box.
 * @param click The click
 * @param selectMode Whether any monitor is ticked
 * @param selectable Whether the row has a box
 * @returns What to do
 */
export function rowClickAction(click: RowClick, selectMode: boolean, selectable: boolean): RowClickAction {
    if (click.button !== 0 || click.ctrlKey || click.metaKey || click.shiftKey || click.altKey) {
        return "browser";
    }
    if (click.detail === 0) {
        return "navigate";
    }
    return selectMode && selectable ? "toggle" : "navigate";
}
