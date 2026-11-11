// A realtime request with a time limit: a request that's never answered, such as on a lost
// connection, mustn't leave the page waiting, nor be taken for a refusal.

export type Answer<T> = { kind: "answered"; res: T } | { kind: "unknown" };

type Emit = (event: string, ...args: unknown[]) => void;

export const REQUEST_TIMEOUT_MS = 30_000;

/**
 * Send an event and wait for its answer, or for the time limit
 * @param emit Sends the event, as the socket's emit does, with the answer's callback last
 * @param event The event
 * @param args Its arguments
 * @param timeoutMs How long to wait
 * @returns The answer, or unknown when there was none in time. A late answer is ignored
 */
export function request<T>(
    emit: Emit,
    event: string,
    args: unknown[],
    timeoutMs = REQUEST_TIMEOUT_MS
): Promise<Answer<T>> {
    return new Promise((resolve) => {
        let settled = false;
        const timer = setTimeout(() => {
            settled = true;
            resolve({ kind: "unknown" });
        }, timeoutMs);
        emit(event, ...args, (res: T) => {
            if (!settled) {
                settled = true;
                clearTimeout(timer);
                resolve({ kind: "answered", res });
            }
        });
    });
}
