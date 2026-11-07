// The app icon each browser shows: the header logo and the tab's icon. It's a per-browser choice,
// kept in localStorage like the theme. Status pages keep their own logo and icon.

export const appIcons = [
    { id: "honey-two-tone", name: "Honey two-tone", src: "/icon.svg" },
    { id: "honey", name: "Honey", src: "/icons/honey.svg" },
    { id: "kuma-green", name: "Kuma green", src: "/icons/kuma-green.svg" },
    { id: "green-two-tone", name: "Green two-tone", src: "/icons/green-two-tone.svg" },
    { id: "ocean", name: "Ocean", src: "/icons/ocean.svg" },
    { id: "violet", name: "Violet", src: "/icons/violet.svg" },
    { id: "classic", name: "Classic", src: "/icons/classic.svg" },
];

export const defaultAppIcon = appIcons[0].id;

/**
 * The browser's chosen icon, or the default
 * @returns {string} Icon id
 */
export function storedAppIcon() {
    try {
        const id = localStorage.appIcon;
        return appIcons.some((icon) => icon.id === id) ? id : defaultAppIcon;
    } catch (e) {
        return defaultAppIcon;
    }
}

/**
 * The address of an icon's image
 * @param {string} id Icon id
 * @returns {string} Image URL
 */
export function appIconSrc(id) {
    return (appIcons.find((icon) => icon.id === id) ?? appIcons[0]).src;
}

/**
 * Point the tab's icon at the chosen one. Status pages set their own, so they're left alone.
 * @param {string} id Icon id
 * @returns {void}
 */
export function applyFavicon(id) {
    if (location.pathname.startsWith("/status")) {
        return;
    }
    const link = document.querySelector("link[rel~='icon']");
    if (link) {
        link.setAttribute("type", "image/svg+xml");
        link.setAttribute("href", appIconSrc(id));
    }
}

// Set before the favicon badge reads the icon, when this module is first imported.
applyFavicon(storedAppIcon());
