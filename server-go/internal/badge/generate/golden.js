// Usage: node golden.js <node_modules> <out.json>
const path = require("path");
const mods = process.argv[2];
const { makeBadge } = require(path.join(mods, "badge-maker"));
let chroma = require(path.join(mods, "chroma-js"));
chroma = chroma.default || chroma;
const styles = ["flat", "flat-square", "plastic", "for-the-badge", "social"];
const texts = [
    ["Status", "Up"],
    ["Uptime (24h)", "99.95%"],
    ["", "N/A"],
    ["Avg. Ping (720h)", "123ms"],
    ["Cert Exp.", "12 days"],
    ["", ""],
    ["Résumé ünïcödé", "日本語のテキスト"],
    ["emoji 🚀", "ok ✅"],
    ["a < b & c > d", "\"quoted\" 'single'"],
    ["  padded  ", "  spaced  "],
    ["tab\there", "x"],
    ["Status", "Maintenance"],
    ["Wide WWWWWWWWWW", "iiiiiiiii"],
    ["", "Degraded"],
    ["Response", "0ms"],
];
const colors = [
    undefined,
    "",
    "#66c20a",
    "66c20a",
    "#C2290A",
    "abc",
    "brightgreen",
    "critical",
    "lightgray",
    "red",
    "white",
    "rgb(10, 200, 30)",
    "rgba(255,255,255,0.5)",
    "hsl(120, 50%, 50%)",
    "#ffff",
    "not-a-color",
    "Yellow",
    "#999",
    "#f8a306",
    "#1747f5",
    "blue",
    "rgb(50% 50% 50%)",
    "#fff",
];
const cases = [];
let i = 0;
for (const style of styles) {
    for (const [label, message] of texts) {
        const color = colors[i % colors.length];
        const labelColor = colors[(i * 7 + 3) % colors.length];
        i++;
        const input = { style, label, message };
        if (color !== undefined) {
            input.color = color;
        }
        if (labelColor !== undefined) {
            input.labelColor = labelColor;
        }
        cases.push({ input, svg: makeBadge(input) });
    }
    for (const color of colors) {
        const input = { style, label: "Status", message: "Up" };
        if (color !== undefined) {
            input.color = color;
            input.labelColor = color;
        }
        cases.push({ input, svg: makeBadge(input) });
        const noLabel = { style, label: "", message: "Up" };
        if (color !== undefined) {
            noLabel.labelColor = color;
        }
        cases.push({ input: noLabel, svg: makeBadge(noLabel) });
    }
}
const percentages = [];
for (let p = 0; p <= 1.0000001; p += 0.0125) {
    percentages.push(p);
}
percentages.push(0.99987, 0.5, 0.333333, 1);
const colorsOut = percentages.map((p) => ({ p, hex: chroma(`hsl(${p * 80 + 10}, 90%, 40%)`).hex() }));
require("fs").writeFileSync(process.argv[3], JSON.stringify({ badges: cases, percentageColors: colorsOut }, null, 1));
console.log(cases.length, "badges,", colorsOut.length, "colours");
