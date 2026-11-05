/**
 * Realtime client: Server-Sent Events from the server, and POST requests to the server.
 * Its API is the part of socket.io-client that the app uses.
 *
 * The stream is read with fetch instead of EventSource, because EventSource can't send
 * headers, which non-browser clients need for cookies and API keys.
 */

const REALTIME_PATH = "/api/realtime";
const MIN_RETRY_MS = 1000;
const MAX_RETRY_MS = 5000;
// The server sends a keep-alive every 25 seconds, so silence means a dead connection.
const STALE_AFTER_MS = 60 * 1000;
// Browsers limit the total size of keepalive requests to 64 KB.
const MAX_KEEPALIVE_BYTES = 60 * 1024;

type Listener = (...args: any[]) => void;

interface Message {
    event: string;
    args: unknown[];
    ack?: number;
}

interface Options {
    /** Extra request headers, for clients that aren't browsers */
    extraHeaders?: Record<string, string>;
    /** Whether to retry after losing the connection */
    reconnection?: boolean;
}

export class RealtimeClient {
    id: string | undefined;
    connected = false;
    [key: string]: any;

    private listeners = new Map<string, Listener[]>();
    private acks = new Map<number, Listener>();
    private nextAck = 1;
    private queue: Message[] = [];
    private sending = false;
    private abort: AbortController | null = null;
    private active = false;
    private retryMs = MIN_RETRY_MS;
    private retryTimer: ReturnType<typeof setTimeout> | null = null;
    private staleTimer: ReturnType<typeof setTimeout> | null = null;

    /**
     * @param baseUrl Backend URL, empty for the current origin
     * @param options Connection options
     */
    constructor(
        private readonly baseUrl: string,
        private readonly options: Options = {}
    ) {
        this.connect();
    }

    on(event: string, listener: Listener): this {
        const list = this.listeners.get(event) ?? [];
        list.push(listener);
        this.listeners.set(event, list);
        return this;
    }

    once(event: string, listener: Listener): this {
        const wrapper: Listener = (...args) => {
            this.off(event, wrapper);
            listener(...args);
        };
        return this.on(event, wrapper);
    }

    off(event: string, listener?: Listener): this {
        this.listeners.set(
            event,
            listener ? (this.listeners.get(event) ?? []).filter((item) => item !== listener) : []
        );
        return this;
    }

    /**
     * Send an event. A function as the last argument receives the server's reply. Events
     * sent while disconnected wait for the next connection.
     */
    emit(event: string, ...args: unknown[]): this {
        const message: Message = { event, args };
        const last = args[args.length - 1];
        if (typeof last === "function") {
            message.args = args.slice(0, -1);
            message.ack = this.nextAck++;
            this.acks.set(message.ack, last as Listener);
        }
        this.queue.push(message);
        if (this.connected) {
            queueMicrotask(() => this.flush());
        }
        return this;
    }

    connect(): this {
        if (!this.active) {
            this.active = true;
            this.retryMs = MIN_RETRY_MS;
            void this.run();
        }
        return this;
    }

    disconnect(): this {
        this.active = false;
        if (this.retryTimer) {
            clearTimeout(this.retryTimer);
            this.retryTimer = null;
        }
        this.drop();
        return this;
    }

    close(): this {
        return this.disconnect();
    }

    private fire(event: string, ...args: unknown[]): void {
        for (const listener of [...(this.listeners.get(event) ?? [])]) {
            listener(...args);
        }
    }

    /** Leave the current connection. Replies that are still due can't arrive anymore. */
    private drop(): void {
        this.abort?.abort();
        this.abort = null;
        if (this.staleTimer) {
            clearTimeout(this.staleTimer);
            this.staleTimer = null;
        }
        this.acks.clear();
        if (this.connected) {
            this.connected = false;
            this.id = undefined;
            this.fire("disconnect");
        }
    }

    private markAlive(): void {
        if (this.staleTimer) {
            clearTimeout(this.staleTimer);
        }
        this.staleTimer = setTimeout(() => this.abort?.abort(), STALE_AFTER_MS);
        (this.staleTimer as { unref?: () => void }).unref?.();
    }

    private async run(): Promise<void> {
        const abort = new AbortController();
        this.abort = abort;
        try {
            const res = await fetch(`${this.baseUrl}${REALTIME_PATH}/stream`, {
                credentials: "include",
                headers: { Accept: "text/event-stream", ...this.options.extraHeaders },
                signal: abort.signal,
                cache: "no-store",
            });
            if (!res.ok || !res.body) {
                throw new Error(`The server responded with status ${res.status}`);
            }
            await this.read(res.body, abort);
            if (this.abort === abort) {
                this.drop();
            }
        } catch (e: any) {
            if (abort.signal.aborted && this.abort !== abort) {
                return; // Left on purpose, and a newer connection may be running already.
            }
            const wasConnected = this.connected;
            this.drop();
            if (!wasConnected) {
                this.fire("connect_error", e);
            }
        }

        if (this.active && this.abort === null && this.options.reconnection !== false) {
            this.retryTimer = setTimeout(() => {
                this.retryTimer = null;
                if (this.active) {
                    void this.run();
                }
            }, this.retryMs);
            this.retryMs = Math.min(this.retryMs * 2, MAX_RETRY_MS);
        }
    }

    private async read(body: ReadableStream<Uint8Array>, abort: AbortController): Promise<void> {
        const reader = body.getReader();
        const decoder = new TextDecoder();
        let buffer = "";
        this.markAlive();

        while (true) {
            const { done, value } = await reader.read();
            if (done || this.abort !== abort) {
                return;
            }
            this.markAlive();
            buffer += decoder.decode(value, { stream: true });

            let end: number;
            while ((end = buffer.indexOf("\n\n")) !== -1) {
                const block = buffer.slice(0, end);
                buffer = buffer.slice(end + 2);
                this.handleBlock(block);
                if (this.abort !== abort) {
                    return; // A listener disconnected.
                }
            }
        }
    }

    private handleBlock(block: string): void {
        let event = "";
        let data = "";
        for (const line of block.split("\n")) {
            if (line.startsWith("event: ")) {
                event = line.slice(7);
            } else if (line.startsWith("data: ")) {
                data += line.slice(6);
            }
        }
        if (!data) {
            return;
        }
        const payload = JSON.parse(data);

        if (event === "open") {
            this.id = payload.id;
            this.connected = true;
            this.retryMs = MIN_RETRY_MS;
            this.fire("connect");
            this.flush();
        } else if (event === "ack") {
            const [ackId, ...args] = payload;
            const callback = this.acks.get(ackId);
            this.acks.delete(ackId);
            callback?.(...args);
        } else if (event === "message") {
            const [name, ...args] = payload;
            this.fire(name, ...args);
        }
    }

    /** Send queued events in one request at a time, which keeps them in order */
    private async flush(): Promise<void> {
        if (this.sending || !this.connected || this.queue.length === 0) {
            return;
        }
        this.sending = true;
        const id = this.id;
        const messages = this.queue;
        this.queue = [];

        try {
            const body = JSON.stringify({ id, messages });
            const res = await fetch(`${this.baseUrl}${REALTIME_PATH}/send`, {
                method: "POST",
                credentials: "include",
                headers: { "Content-Type": "application/json", ...this.options.extraHeaders },
                body,
                // Lets an event that was just sent survive the user leaving the page
                keepalive: body.length <= MAX_KEEPALIVE_BYTES,
            });
            if (!res.ok) {
                throw new Error(`The server responded with status ${res.status}`);
            }
        } catch (e: any) {
            // The server may have run these already, so they aren't sent again.
            console.error(`Failed to send events: ${e?.message}`);
            if (this.id === id) {
                this.abort?.abort();
            }
        } finally {
            this.sending = false;
        }
        if (this.queue.length > 0) {
            void this.flush();
        }
    }
}

/**
 * Connect to the backend
 * @param baseUrl Backend URL, empty for the current origin
 * @param options Connection options
 * @returns The client
 */
export function connectRealtime(baseUrl = "", options: Options = {}): RealtimeClient {
    return new RealtimeClient(baseUrl, options);
}
