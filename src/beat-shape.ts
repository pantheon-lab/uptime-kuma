// How the heartbeat bar draws a beat, so that its status shows by shape as well as colour, and its
// edge stands out from the background (WCAG 1.4.11 asks 3:1 for parts of graphics).
import { DOWN, MAINTENANCE, PENDING, UP } from "./util";

export interface BeatShape {
    /** Height, as a share of the full beat */
    height: number;
    /** A gap across the middle: a down beat */
    notch: boolean;
    /** Outline only: maintenance */
    hollow: boolean;
    /** An outline in this colour, where the fill alone is too pale for the background */
    edge: string | null;
    /** A fill colour in place of the theme's, where the theme's is too dark for the background */
    fill: string | null;
}

// Darker shades of the up and pending colours, 3.4:1 and 3.3:1 on white
export const UP_EDGE_LIGHT = "#2e9e5b";
export const PENDING_EDGE_LIGHT = "#c27f04";
// A lighter maintenance blue, 5.4:1 on the dark background, where the usual one is 2.9:1
export const MAINTENANCE_DARK = "#5b80ff";

/**
 * The shape of a beat
 * @param status The beat's status, or null for an empty slot
 * @param dark Whether the dark theme is on
 * @returns How to draw it
 */
export function beatShape(status: number | null, dark: boolean): BeatShape {
    const shape: BeatShape = { height: 1, notch: false, hollow: false, edge: null, fill: null };
    switch (status) {
        case UP:
            shape.edge = dark ? null : UP_EDGE_LIGHT;
            break;
        case DOWN:
            shape.notch = true;
            break;
        case PENDING:
            shape.height = 0.6;
            shape.edge = dark ? null : PENDING_EDGE_LIGHT;
            break;
        case MAINTENANCE:
            shape.hollow = true;
            shape.fill = dark ? MAINTENANCE_DARK : null;
            break;
        default:
            // An empty slot: no data, nothing to tell apart
            break;
    }
    return shape;
}
