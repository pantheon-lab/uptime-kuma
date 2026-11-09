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
 * @returns The callback URL, and whether it was guessed from the page
 */
export function callbackURL(fromServer: string, origin: string): { url: string; guessed: boolean } {
    if (fromServer) {
        return { url: fromServer, guessed: false };
    }
    return { url: origin.replace(/\/+$/, "") + "/api/auth/github/callback", guessed: true };
}
