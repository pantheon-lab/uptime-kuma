// Works out the next release from the Conventional Commits since the latest v* tag, for
// .github/workflows/release.yml, and writes its release notes.
//
// Usage: node extra/release-version.mjs [auto|patch|minor|major] [notes file]
//
// It prints version=<x.y.z> and release=<true|false>, in the format of $GITHUB_OUTPUT.
//  - "feat!:", "fix(x)!:" or a "BREAKING CHANGE:" footer is a major release
//  - "feat:" is a minor release
//  - "fix:", "perf:", "revert:" and dependency updates ("chore(deps):", "build(deps):") are patch releases
//  - anything else, such as docs, test, ci, chore and refactor, makes no release on its own
// Without a v* tag yet, the first release is package.json's version without its pre-release part.
import { execFileSync } from "node:child_process";
import { readFileSync, writeFileSync } from "node:fs";

const LEVELS = ["none", "patch", "minor", "major"];
const SECTIONS = [
    ["major", "Breaking changes"],
    ["feat", "Features"],
    ["fix", "Fixes"],
    ["perf", "Performance"],
    ["revert", "Reverts"],
    ["deps", "Dependencies"],
];

/**
 * Run git and return its output
 * @param {...string} args Arguments
 * @returns {string} Output, trimmed
 */
function git(...args) {
    return execFileSync("git", args, { encoding: "utf8" }).trim();
}

/**
 * Classify a commit by its Conventional Commits header
 * @param {string} subject First line
 * @param {string} body The rest
 * @returns {{level: string, section: string|null, text: string}} Release level, notes section and text
 */
export function classify(subject, body) {
    const m = /^(\w+)(?:\(([^)]*)\))?(!)?:\s*(.+)$/.exec(subject);
    if (!m) {
        return { level: "none", section: null, text: subject };
    }
    const [, type, scope, bang, text] = m;
    const entry = scope ? `**${scope}:** ${text}` : text;
    if (bang || /^BREAKING[ -]CHANGE:/m.test(body)) {
        return { level: "major", section: "major", text: entry };
    }
    if (type === "feat") {
        return { level: "minor", section: "feat", text: entry };
    }
    if (type === "fix" || type === "perf" || type === "revert") {
        return { level: "patch", section: type, text: entry };
    }
    if ((type === "chore" || type === "build") && scope === "deps") {
        return { level: "patch", section: "deps", text };
    }
    return { level: "none", section: null, text: entry };
}

/**
 * Bump a version
 * @param {string} version x.y.z
 * @param {string} level patch, minor or major
 * @returns {string} The next version
 */
export function bump(version, level) {
    const [major, minor, patch] = version.split(".").map(Number);
    if (level === "major") {
        return `${major + 1}.0.0`;
    }
    if (level === "minor") {
        return `${major}.${minor + 1}.0`;
    }
    return `${major}.${minor}.${patch + 1}`;
}

/**
 * Work out the release and write its notes
 * @param {string} forced auto, patch, minor or major
 * @param {string} notesFile Where to write the notes
 * @returns {{version: string, release: boolean}} The release
 */
function main(forced, notesFile) {
    const tags = git("tag", "--list", "v[0-9]*.[0-9]*.[0-9]*", "--sort=-v:refname", "--merged", "HEAD")
        .split("\n")
        .filter((t) => /^v\d+\.\d+\.\d+$/.test(t));
    const latest = tags[0];

    if (!latest) {
        const pkg = JSON.parse(readFileSync("package.json", "utf8"));
        const version = pkg.version.replace(/-.*$/, "");
        writeFileSync(notesFile, `The first release of Pantheon Lab's Uptime Kuma, ${version}.\n`);
        return { version, release: true };
    }

    // Each commit is "<hash>\x1f<subject>\x1f<body>\x1e"; merge commits only repeat their branches'
    const log = git("log", "--no-merges", "--format=%h%x1f%s%x1f%b%x1e", `${latest}..HEAD`);
    const commits = log
        .split("\x1e")
        .map((c) => c.trim())
        .filter(Boolean)
        .map((c) => {
            const [hash, subject, body = ""] = c.split("\x1f");
            return { hash, ...classify(subject, body) };
        });

    let level = commits.reduce((acc, c) => (LEVELS.indexOf(c.level) > LEVELS.indexOf(acc) ? c.level : acc), "none");
    if (forced && forced !== "auto") {
        level = forced;
    }
    if (level === "none") {
        return { version: latest.slice(1), release: false };
    }

    const version = bump(latest.slice(1), level);
    const repo = process.env.GITHUB_REPOSITORY;
    let notes = "";
    for (const [key, title] of SECTIONS) {
        const items = commits.filter((c) => c.section === key);
        if (items.length > 0) {
            notes += `## ${title}\n\n${items.map((c) => `- ${c.text} (${c.hash})`).join("\n")}\n\n`;
        }
    }
    if (!notes) {
        notes = "No feature or fix commits; this release was made by hand.\n\n";
    }
    if (repo) {
        notes += `**Full changelog:** https://github.com/${repo}/compare/${latest}...v${version}\n`;
    }
    writeFileSync(notesFile, notes);
    return { version, release: true };
}

if (import.meta.url === `file://${process.argv[1]}`) {
    const [forced = "auto", notesFile = "release-notes.md"] = process.argv.slice(2);
    if (!["auto", ...LEVELS.slice(1)].includes(forced)) {
        console.error(`unknown bump "${forced}": use auto, patch, minor or major`);
        process.exit(2);
    }
    const { version, release } = main(forced, notesFile);
    console.log(`version=${version}`);
    console.log(`release=${release}`);
}
