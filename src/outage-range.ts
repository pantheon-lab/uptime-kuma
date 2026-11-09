/**
 * The range of time to chart for an outage: the outage, with some time either side to show how
 * the monitor was before and after.
 */

export interface OutageTimes {
    /** "2026-11-08 01:26:34.123", in UTC, as the server sends it */
    start: string;
    end: string | null;
}

// A quarter of the outage either side, and at least this much
const MIN_MARGIN_MS = 15 * 60 * 1000;

/**
 * @param {string} time Server time, in UTC
 * @returns {Date} The time
 */
function parseTime(time: string): Date {
    return new Date(time.includes("T") ? time : time.replace(" ", "T") + "Z");
}

/**
 * The range to chart for an outage, ending no later than now
 * @param {OutageTimes} outage The outage; one that hasn't ended runs to now
 * @param {Date} now Now
 * @returns {{from: Date, to: Date}} The range
 */
export function outageChartRange(outage: OutageTimes, now: Date): { from: Date; to: Date } {
    const start = parseTime(outage.start).getTime();
    const end = outage.end ? parseTime(outage.end).getTime() : now.getTime();
    const margin = Math.max(MIN_MARGIN_MS, (end - start) / 4);
    return {
        from: new Date(start - margin),
        to: new Date(Math.min(end + margin, now.getTime())),
    };
}
