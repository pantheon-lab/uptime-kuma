// Settings > Single Sign-On: the GitHub teams as the form shows them, and the address GitHub's
// OAuth App sends people back to.

/**
 * Team slugs from the form's text: lowercase, split by commas or spaces, without repeats
 * @param text Such as "sre, Support"
 * @returns Such as ["sre", "support"]
 */
export function parseTeams(text: string): string[] {
    const out: string[] = [];
    for (const team of text.toLowerCase().split(/[\s,]+/)) {
        if (team !== "" && !out.includes(team)) {
            out.push(team);
        }
    }
    return out;
}

/**
 * Emails or domains from the form's text, as for teams: lowercase, split by commas or spaces
 */
export const parseList = parseTeams;

/**
 * Teams as the form shows them
 * @param teams Team slugs
 * @returns Such as "sre, support"
 */
export function teamsText(teams: string[]): string {
    return teams.join(", ");
}

/**
 * The OAuth App's callback URL: from the server's Primary Base URL, or else this page's address
 * @param fromServer The server's, or "" when it has no base URL
 * @param origin This page's origin
 * @param provider Whose callback
 * @returns The callback URL, and whether it was guessed from the page
 */
export function callbackURL(
    fromServer: string,
    origin: string,
    provider: "github" | "google" = "github"
): { url: string; guessed: boolean } {
    if (fromServer) {
        return { url: fromServer, guessed: false };
    }
    return { url: origin.replace(/\/+$/, "") + `/api/auth/${provider}/callback`, guessed: true };
}

/**
 * Groups or scopes from the form's text, as the provider writes them: split by commas, trimmed,
 * keeping their case and spaces, without repeats
 * @param text Such as "Kuma Admins, 0f1e…"
 * @returns Such as ["Kuma Admins", "0f1e…"]
 */
export function parseGroups(text: string): string[] {
    const out: string[] = [];
    for (const group of text.split(",")) {
        const g = group.trim();
        if (g !== "" && !out.includes(g)) {
            out.push(g);
        }
    }
    return out;
}
