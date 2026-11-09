// Status pages: when coming maintenance happens, in the reader's own time, and the links that
// subscription emails carry back to the page.

export interface SubscriptionLink {
    action: "confirm" | "unsubscribe";
    token: string;
}

/**
 * The subscription action a page was opened for, from its query
 * @param query The route's query
 * @returns The action and its token, or null
 */
export function subscriptionLink(query: Record<string, unknown>): SubscriptionLink | null {
    const action = query.subscription;
    const token = query.token;
    if ((action !== "confirm" && action !== "unsubscribe") || typeof token !== "string") {
        return null;
    }
    return /^[A-Za-z0-9_-]{16,128}$/.test(token) ? { action, token } : null;
}

/**
 * When a window runs, in the reader's time zone: one day's date once if it ends that day
 * @param start When it starts, as ISO 8601
 * @param end When it ends
 * @param locale The UI's language
 * @param timeZone A time zone, or the browser's
 * @returns Such as "Tue, Nov 14, 02:00 – 04:00 GMT+8"
 */
export function upcomingWhen(start: string, end: string, locale: string, timeZone?: string): string {
    const format = new Intl.DateTimeFormat(locale, {
        weekday: "short",
        month: "short",
        day: "numeric",
        hour: "2-digit",
        minute: "2-digit",
        timeZoneName: "short",
        timeZone,
    });
    return format.formatRange(new Date(start), new Date(end));
}

/**
 * How soon a window starts
 * @param start When it starts, as ISO 8601
 * @param now The time now
 * @param locale The UI's language
 * @returns Such as "in 3 hours" or "in 2 days"
 */
export function startsIn(start: string, now: Date, locale: string): string {
    const minutes = Math.max(1, Math.round((new Date(start).getTime() - now.getTime()) / 60000));
    const format = new Intl.RelativeTimeFormat(locale, { numeric: "auto" });
    if (minutes < 60) {
        return format.format(minutes, "minute");
    }
    if (minutes < 48 * 60) {
        return format.format(Math.round(minutes / 60), "hour");
    }
    return format.format(Math.round(minutes / (24 * 60)), "day");
}

export interface NotificationItem {
    id: number;
    name: string;
    config?: string;
}

/**
 * The email (SMTP) notifications, which a page's emails can go through. A notification's type is
 * in its config, which the server sends as JSON text.
 * @param list The UI's notifications
 * @returns Those that send email, by name
 */
export function smtpNotifications(list: NotificationItem[]): NotificationItem[] {
    return list
        .filter((n) => {
            try {
                return JSON.parse(n.config ?? "{}").type === "smtp";
            } catch {
                return false;
            }
        })
        .sort((a, b) => a.name.localeCompare(b.name));
}
