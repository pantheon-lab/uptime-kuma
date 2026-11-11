// Made-up heartbeats for the Appearance settings' preview, in the server's format.

import { DOWN, UP } from "./util";

export interface PreviewBeat {
    status: number;
    // UTC, as "2026-11-10 09:30:00.000"
    time: string;
    msg: string;
    ping: number | null;
}

/**
 * Beats every minute up to now, oldest first
 * @param now The newest beat's time
 * @param count How many
 * @param statuses Status of some beats, by how many minutes before now; the rest are up
 * @param downMsg The message of a down beat
 * @returns Beats
 */
export function previewBeats(
    now: Date,
    count: number,
    statuses: Record<number, number>,
    downMsg: string
): PreviewBeat[] {
    const beats: PreviewBeat[] = [];
    for (let ago = count - 1; ago >= 0; ago--) {
        const status = statuses[ago] ?? UP;
        const time = new Date(now.getTime() - ago * 60_000);
        beats.push({
            status,
            time: time.toISOString().replace("T", " ").slice(0, 23),
            msg: status === DOWN ? downMsg : "",
            ping: status === UP ? 40 + ((ago * 7) % 30) : null,
        });
    }
    return beats;
}
