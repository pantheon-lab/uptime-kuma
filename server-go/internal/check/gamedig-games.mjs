// Writes gamedig-games.json: the games whose protocols the Go GameDig check speaks, from gamedig's list (MIT).
// Usage: node gamedig-games.mjs <gamedig package>/lib/games.js gamedig-games.json
import fs from "node:fs";
const { games } = await import(process.argv[2]);
const keep = new Set(["valve", "gamespy1", "gamespy2", "gamespy3", "quake2", "quake3", "ase", "minecraft"]);
const out = [];
for (const [key, g] of Object.entries(games)) {
    const o = g.options;
    if (!keep.has(o.protocol)) {
        continue;
    }
    const entry = { key, name: g.name, protocol: o.protocol, port: o.port ?? 0 };
    if (o.port_query) {
        entry.portQuery = o.port_query;
    }
    if (o.port_query_offset !== undefined) {
        entry.portQueryOffset = [].concat(o.port_query_offset);
    }
    out.push(entry);
}
out.sort((a, b) => a.name.localeCompare(b.name));
fs.writeFileSync(process.argv[3], JSON.stringify(out, null, 1) + "\n");
console.log(out.length, "games");
