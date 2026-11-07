/**
 * Searching the command palette's entries: monitors, status pages, maintenance windows, settings
 * pages and actions.
 */

export interface Command {
    id: string;
    label: string;
    /** Shown beside the label, such as "Monitor" */
    group: string;
    /** Where it goes */
    to: string;
    /** Other words it's found by */
    keywords?: string;
}

/**
 * How well a text matches a query: higher is better, -1 is no match. A match at the start, then
 * at the start of a word, then anywhere, then the query's letters in order.
 * @param {string} text Text, in lower case
 * @param {string} query Query, in lower case
 * @returns {number} Score
 */
export function matchScore(text: string, query: string): number {
    if (query === "") {
        return 0;
    }
    const at = text.indexOf(query);
    if (at === 0) {
        return 400 - text.length / 100;
    }
    if (at > 0) {
        const wordStart = /[\s/\-_.([]/.test(text[at - 1]);
        return (wordStart ? 300 : 200) - at / 100;
    }
    // The query's letters in order, closer together scoring higher
    let from = 0;
    let gaps = 0;
    for (const ch of query) {
        const found = text.indexOf(ch, from);
        if (found < 0) {
            return -1;
        }
        gaps += found - from;
        from = found + 1;
    }
    return Math.max(1, 100 - gaps);
}

/**
 * The commands that match a query, best first, keeping their order among equals
 * @param {Command[]} commands Commands
 * @param {string} query What was typed
 * @param {number} limit How many at most
 * @returns {Command[]} Matches
 */
export function searchCommands(commands: Command[], query: string, limit = 50): Command[] {
    const q = query.trim().toLowerCase();
    if (q === "") {
        return commands.slice(0, limit);
    }
    const scored: { command: Command; score: number; index: number }[] = [];
    commands.forEach((command, index) => {
        const label = matchScore(command.label.toLowerCase(), q);
        const keywords = command.keywords ? matchScore(command.keywords.toLowerCase(), q) - 50 : -1;
        const score = Math.max(label, keywords);
        if (score >= 0) {
            scored.push({ command, score, index });
        }
    });
    scored.sort((a, b) => b.score - a.score || a.index - b.index);
    return scored.slice(0, limit).map((s) => s.command);
}
