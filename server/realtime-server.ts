import { randomUUID } from "node:crypto";
import zlib from "node:zlib";
import express from "express";
import type { IncomingHttpHeaders, IncomingMessage } from "node:http";
import { log } from "../src/util";

/** Base path of the realtime endpoints */
export const REALTIME_PATH = "/api/realtime";

const KEEP_ALIVE_MS = 25 * 1000;
const MAX_BODY = "32mb";
// A client this far behind is dropped. It reloads its state when it reconnects.
const MAX_BUFFERED_BYTES = 64 * 1024 * 1024;

type Listener = (...args: any[]) => unknown;
type Middleware = (packet: unknown[], next: () => void) => void;

interface RealtimeOptions {
    /** Decide whether a request's origin may connect */
    isOriginAllowed: (req: IncomingMessage) => Promise<boolean>;
    /** Origins allowed to read responses across origins. Only used in development. */
    corsOrigin?: (origin: string) => boolean;
}

/**
 * One connected browser. The API is the part of socket.io's Socket that the handlers use,
 * so the handlers don't depend on the transport.
 */
export class RealtimeSocket {
    readonly id = randomUUID();
    readonly request: { headers: IncomingHttpHeaders };
    readonly client: { conn: { remoteAddress: string | undefined; request: { headers: IncomingHttpHeaders } } };
    [key: string]: any;

    private listeners = new Map<string, Listener[]>();
    private middlewares: Middleware[] = [];
    private rooms = new Set<string>();

    /**
     * @param server Owning server
     * @param req Request that opened the stream
     * @param send Writes one server-sent event
     * @param close Ends the stream
     */
    constructor(
        private readonly server: RealtimeServer,
        req: IncomingMessage,
        private readonly send: (event: string, data: string) => void,
        private readonly close: () => void
    ) {
        this.request = { headers: req.headers };
        this.client = { conn: { remoteAddress: req.socket.remoteAddress, request: { headers: req.headers } } };
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
            return listener(...args);
        };
        return this.on(event, wrapper);
    }

    off(event: string, listener: Listener): this {
        this.listeners.set(
            event,
            (this.listeners.get(event) ?? []).filter((item) => item !== listener)
        );
        return this;
    }

    use(middleware: Middleware): this {
        this.middlewares.push(middleware);
        return this;
    }

    emit(event: string, ...args: unknown[]): boolean {
        this.sendEncoded(JSON.stringify([event, ...args]));
        return true;
    }

    /** @internal Used by room broadcasts, to serialize a payload only once */
    sendEncoded(encoded: string): void {
        this.send("message", encoded);
    }

    join(room: string | number): void {
        this.rooms.add(String(room));
        this.server.addToRoom(String(room), this.id);
    }

    leave(room: string | number): void {
        this.rooms.delete(String(room));
        this.server.removeFromRoom(String(room), this.id);
    }

    disconnect(): this {
        this.close();
        return this;
    }

    /** @internal Handle one message from the browser */
    receive(event: string, args: unknown[], ackId: number | undefined): void {
        const fullArgs = [...args];
        if (ackId !== undefined) {
            let acknowledged = false;
            fullArgs.push((...response: unknown[]) => {
                if (!acknowledged) {
                    acknowledged = true;
                    this.send("ack", JSON.stringify([ackId, ...response]));
                }
            });
        }

        const run = (index: number): void => {
            if (index < this.middlewares.length) {
                this.middlewares[index]([event, ...args], () => run(index + 1));
            } else {
                this.dispatch(event, fullArgs);
            }
        };
        run(0);
    }

    /** @internal Run the listeners, and report failures instead of crashing the server */
    dispatch(event: string, args: unknown[]): void {
        for (const listener of [...(this.listeners.get(event) ?? [])]) {
            try {
                Promise.resolve(listener(...args)).catch((e) => {
                    log.error("realtime", `Handler for "${event}" failed: ${e?.message}`);
                });
            } catch (e: any) {
                log.error("realtime", `Handler for "${event}" failed: ${e?.message}`);
            }
        }
    }

    /** @internal Remove all room memberships */
    leaveAll(): void {
        for (const room of [...this.rooms]) {
            this.leave(room);
        }
    }
}

/**
 * Realtime messaging over plain HTTP: Server-Sent Events from the server to the browser,
 * and POST requests from the browser to the server.
 *
 * Replies travel on the event stream too, so a client sees everything in the order that
 * the server sent it, and a slow handler doesn't hold an HTTP connection open.
 *
 * A POST only works with the connection id, a random value that was sent on the event
 * stream. Another site can't read that stream, which is what protects against CSRF.
 */
export class RealtimeServer {
    readonly sockets = {
        sockets: new Map<string, RealtimeSocket>(),
        adapter: { rooms: new Map<string, Set<string>>() },
    };

    private connectionListeners: Array<(socket: RealtimeSocket) => unknown> = [];

    /**
     * @param app Express app to add the endpoints to
     * @param options Origin rules
     */
    constructor(
        app: express.Express,
        private readonly options: RealtimeOptions
    ) {
        app.options(`${REALTIME_PATH}/*`, (req, res) => {
            this.applyCors(req, res);
            res.status(204).end();
        });
        app.get(`${REALTIME_PATH}/stream`, (req, res) => {
            this.handleStream(req, res).catch((e) => {
                log.error("realtime", `Stream failed: ${e?.message}`);
                res.destroy();
            });
        });
        app.post(`${REALTIME_PATH}/send`, express.json({ limit: MAX_BODY }), (req, res) => {
            this.handleSend(req, res).catch((e) => {
                log.error("realtime", `Send failed: ${e?.message}`);
                res.status(500).end();
            });
        });
    }

    on(event: "connection", listener: (socket: RealtimeSocket) => unknown): this {
        if (event === "connection") {
            this.connectionListeners.push(listener);
        }
        return this;
    }

    to(room: string | number) {
        return {
            emit: (event: string, ...args: unknown[]): boolean => {
                const members = this.sockets.adapter.rooms.get(String(room));
                if (members?.size) {
                    const encoded = JSON.stringify([event, ...args]);
                    for (const id of members) {
                        this.sockets.sockets.get(id)?.sendEncoded(encoded);
                    }
                }
                return true;
            },
        };
    }

    /** @internal */
    addToRoom(room: string, socketId: string): void {
        const members = this.sockets.adapter.rooms.get(room) ?? new Set<string>();
        members.add(socketId);
        this.sockets.adapter.rooms.set(room, members);
    }

    /** @internal */
    removeFromRoom(room: string, socketId: string): void {
        const members = this.sockets.adapter.rooms.get(room);
        members?.delete(socketId);
        if (members?.size === 0) {
            this.sockets.adapter.rooms.delete(room);
        }
    }

    /** End every stream, so that the HTTP server can shut down */
    close(): void {
        for (const socket of [...this.sockets.sockets.values()]) {
            socket.disconnect();
        }
    }

    private applyCors(req: express.Request, res: express.Response): void {
        const origin = req.headers.origin;
        if (origin && this.options.corsOrigin?.(origin)) {
            res.setHeader("Access-Control-Allow-Origin", origin);
            res.setHeader("Access-Control-Allow-Credentials", "true");
            res.setHeader("Access-Control-Allow-Methods", "GET, POST, OPTIONS");
            res.setHeader("Access-Control-Allow-Headers", "Content-Type, Authorization");
            res.setHeader("Vary", "Origin");
        }
    }

    private async handleStream(req: express.Request, res: express.Response): Promise<void> {
        this.applyCors(req, res);
        if (!(await this.options.isOriginAllowed(req))) {
            res.status(403).end();
            return;
        }

        res.writeHead(200, {
            "Content-Type": "text/event-stream",
            "Cache-Control": "no-store, no-transform",
            "X-Accel-Buffering": "no",
            ...(/\bgzip\b/.test(String(req.headers["accept-encoding"])) ? { "Content-Encoding": "gzip" } : {}),
        });

        // The login payload is several MB of JSON with thousands of monitors. Every event
        // is flushed, so compression doesn't delay it.
        const gzip = res.getHeader("Content-Encoding") === "gzip" ? zlib.createGzip({ memLevel: 6 }) : null;
        gzip?.pipe(res);

        let closed = false;
        let socket: RealtimeSocket | null = null;

        const write = (text: string): void => {
            if (closed) {
                return;
            }
            if ((gzip?.writableLength ?? 0) + res.writableLength > MAX_BUFFERED_BYTES) {
                log.warn("realtime", "Dropping a client that is too far behind");
                end();
                return;
            }
            if (gzip) {
                gzip.write(text);
                gzip.flush(zlib.constants.Z_SYNC_FLUSH);
            } else {
                res.write(text);
            }
        };

        const end = (): void => {
            if (closed) {
                return;
            }
            closed = true;
            clearInterval(keepAlive);
            if (socket) {
                socket.leaveAll();
                this.sockets.sockets.delete(socket.id);
                socket.dispatch("disconnect", []);
            }
            if (gzip) {
                gzip.end();
            } else {
                res.end();
            }
        };

        const keepAlive = setInterval(() => write(": keep-alive\n\n"), KEEP_ALIVE_MS);
        keepAlive.unref();
        req.on("close", end);

        socket = new RealtimeSocket(this, req, (event, data) => write(`event: ${event}\ndata: ${data}\n\n`), end);
        this.sockets.sockets.set(socket.id, socket);
        write(`event: open\ndata: ${JSON.stringify({ id: socket.id })}\n\n`);

        for (const listener of this.connectionListeners) {
            Promise.resolve(listener(socket)).catch((e) => {
                log.error("realtime", `Connection handler failed: ${e?.message}`);
            });
        }
    }

    private async handleSend(req: express.Request, res: express.Response): Promise<void> {
        this.applyCors(req, res);
        if (!(await this.options.isOriginAllowed(req))) {
            res.status(403).end();
            return;
        }

        const { id, messages } = req.body ?? {};
        if (typeof id !== "string" || !Array.isArray(messages)) {
            res.status(400).end();
            return;
        }
        const socket = this.sockets.sockets.get(id);
        if (!socket) {
            // The stream is gone. The client reconnects, which gives it a new id.
            res.status(410).end();
            return;
        }

        for (const message of messages) {
            const valid =
                message &&
                typeof message.event === "string" &&
                Array.isArray(message.args) &&
                (message.ack === undefined || Number.isInteger(message.ack));
            if (!valid) {
                res.status(400).end();
                return;
            }
        }
        for (const message of messages) {
            socket.receive(message.event, message.args, message.ack);
        }
        res.status(202).end();
    }
}
