import { describe, test, before, after } from "node:test";
import assert from "node:assert";
import http from "node:http";
import express from "express";
import { RealtimeServer } from "../../server/realtime-server";
import { connectRealtime, RealtimeClient } from "../../src/realtime-client";

describe("Realtime transport", () => {
    let server: http.Server;
    let io: RealtimeServer;
    let url: string;
    let originAllowed = true;
    const clients: RealtimeClient[] = [];

    /** @returns A connected client */
    async function connected(options = {}) {
        const client = connectRealtime(url, options);
        clients.push(client);
        await new Promise<void>((resolve, reject) => {
            client.once("connect", () => resolve());
            client.once("connect_error", reject);
        });
        return client;
    }

    /** Wait for a condition */
    async function until(what: string, condition: () => boolean) {
        const deadline = Date.now() + 5000;
        while (!condition()) {
            assert.ok(Date.now() < deadline, `Timed out waiting for ${what}`);
            await new Promise((resolve) => setTimeout(resolve, 5));
        }
    }

    before(async () => {
        const app = express();
        io = new RealtimeServer(app, { isOriginAllowed: async () => originAllowed });
        io.on("connection", async (socket) => {
            socket.join("everyone");
            socket.on("echo", (value, callback) => callback({ echoed: value }));
            socket.on("order", (value, callback) => {
                socket.emit("before-reply", value);
                callback(value);
            });
            socket.on("throws", () => {
                throw new Error("handler bug");
            });
            socket.on("kick", () => socket.disconnect());
        });
        server = http.createServer(app);
        await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
        url = `http://127.0.0.1:${(server.address() as { port: number }).port}`;
    });

    after(async () => {
        clients.forEach((client) => client.disconnect());
        io.close();
        server.closeAllConnections();
        await new Promise((resolve) => server.close(resolve));
    });

    test("delivers a reply, including across compression", async () => {
        const client = await connected();
        const reply = await new Promise((resolve) => client.emit("echo", { a: "é".repeat(100000) }, resolve));
        assert.deepStrictEqual(reply, { echoed: { a: "é".repeat(100000) } });
    });

    test("delivers events before a reply sent after them", async () => {
        const client = await connected();
        const seen: string[] = [];
        client.on("before-reply", () => seen.push("event"));
        await new Promise<void>((resolve) => {
            client.emit("order", 1, () => {
                seen.push("reply");
                resolve();
            });
        });
        assert.deepStrictEqual(seen, ["event", "reply"]);
    });

    test("keeps events in order", async () => {
        const client = await connected();
        const replies: number[] = [];
        for (let i = 0; i < 200; i++) {
            client.emit("echo", i, (reply: { echoed: number }) => replies.push(reply.echoed));
        }
        await until("all replies", () => replies.length === 200);
        assert.deepStrictEqual(replies, [...Array(200).keys()]);
    });

    test("sends room broadcasts only to members", async () => {
        const member = await connected();
        const other = await connected();
        const memberSocket = io.sockets.sockets.get(member.id as string);
        const otherSocket = io.sockets.sockets.get(other.id as string);
        otherSocket?.leave("everyone");
        memberSocket?.join("user-1");

        const got: string[] = [];
        member.on("news", () => got.push("member"));
        other.on("news", () => got.push("other"));
        io.to("user-1").emit("news");
        await until("the broadcast", () => got.length > 0);
        await new Promise((resolve) => setTimeout(resolve, 50));
        assert.deepStrictEqual(got, ["member"]);
        assert.strictEqual(io.sockets.adapter.rooms.get("user-1")?.size, 1);
    });

    test("a failing handler doesn't take the connection down", async () => {
        const client = await connected();
        client.emit("throws");
        const reply = await new Promise((resolve) => client.emit("echo", 1, resolve));
        assert.deepStrictEqual(reply, { echoed: 1 });
    });

    test("removes a closed connection and its rooms", async () => {
        const client = await connected();
        const id = client.id as string;
        assert.ok(io.sockets.sockets.has(id));
        client.disconnect();
        await until("the server to notice", () => !io.sockets.sockets.has(id));
        assert.ok(![...io.sockets.adapter.rooms.values()].some((members) => members.has(id)));
    });

    test("reconnects by itself and sends queued events", async () => {
        const client = await connected();
        const firstId = client.id;
        let disconnects = 0;
        client.on("disconnect", () => disconnects++);
        const reconnected = new Promise<void>((resolve) => client.once("connect", () => resolve()));
        client.emit("kick");
        await until("the disconnect", () => disconnects === 1);
        const reply = new Promise((resolve) => client.emit("echo", "queued", resolve));
        await reconnected;
        assert.notStrictEqual(client.id, firstId);
        assert.deepStrictEqual(await reply, { echoed: "queued" });
    });

    test("rejects a POST without a live connection id", async () => {
        const send = (id: unknown) =>
            fetch(`${url}/api/realtime/send`, {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ id, messages: [{ event: "echo", args: [1] }] }),
            });
        assert.strictEqual((await send("not-a-connection")).status, 410);
        assert.strictEqual((await send(1)).status, 400);
    });

    test("rejects malformed messages without running any of them", async () => {
        const client = await connected();
        const res = await fetch(`${url}/api/realtime/send`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ id: client.id, messages: [{ event: "kick", args: [] }, { event: 1, args: [] }] }),
        });
        assert.strictEqual(res.status, 400);
        await new Promise((resolve) => setTimeout(resolve, 50));
        assert.strictEqual(client.connected, true);
    });

    test("rejects a disallowed origin", async () => {
        originAllowed = false;
        try {
            assert.strictEqual((await fetch(`${url}/api/realtime/stream`)).status, 403);
            const send = await fetch(`${url}/api/realtime/send`, {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ id: "x", messages: [] }),
            });
            assert.strictEqual(send.status, 403);
        } finally {
            originAllowed = true;
        }
    });
});
