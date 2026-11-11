// Pausing or resuming many monitors in one request, setMonitorsActive, with a time limit: a request
// that's never answered, such as on a lost connection, mustn't leave the list's actions disabled.

export type BulkActiveResult =
    | { kind: "done"; count: number }
    | { kind: "failed"; message: string | null }
    // No answer in time: the monitors may have changed or not
    | { kind: "unknown" };

type Emit = (event: string, ids: number[], active: boolean, ack: (res: unknown) => void) => void;

// Long enough for a thousand monitors on a slow database
export const BULK_ACTIVE_TIMEOUT_MS = 30_000;

/**
 * Ask the server to pause or resume monitors, and wait for its answer or the time limit
 * @param emit Sends the event, as the socket's emit does
 * @param ids The monitors, as the list's keys or numbers
 * @param active Resume them, or pause them
 * @param timeoutMs How long to wait for the answer
 * @returns What happened. A late answer is ignored
 */
export function setMonitorsActive(
    emit: Emit,
    ids: Array<string | number>,
    active: boolean,
    timeoutMs = BULK_ACTIVE_TIMEOUT_MS
): Promise<BulkActiveResult> {
    return new Promise((resolve) => {
        let settled = false;
        const settle = (result: BulkActiveResult) => {
            if (!settled) {
                settled = true;
                clearTimeout(timer);
                resolve(result);
            }
        };
        const timer = setTimeout(() => settle({ kind: "unknown" }), timeoutMs);
        emit("setMonitorsActive", ids.map(Number), active, (res) => {
            const reply = (res ?? {}) as { ok?: boolean; count?: number; msg?: string };
            if (reply.ok) {
                settle({ kind: "done", count: Number(reply.count ?? 0) });
            } else {
                settle({ kind: "failed", message: typeof reply.msg === "string" ? reply.msg : null });
            }
        });
    });
}
