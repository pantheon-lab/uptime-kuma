import { ShortcutMatcher } from "../shortcuts";
import { ownsTitle } from "../page-title";

/**
 * The app's keyboard shortcuts, while logged in, on the app's own pages. "?" lists them.
 */
export default {
    created() {
        this.shortcutMatcher = new ShortcutMatcher();
    },

    mounted() {
        window.addEventListener("keydown", this.onShortcutKeydown);
    },

    beforeUnmount() {
        window.removeEventListener("keydown", this.onShortcutKeydown);
    },

    methods: {
        /**
         * @param {KeyboardEvent} e The key press
         * @returns {void}
         */
        onShortcutKeydown(e) {
            if (e.isComposing || e.repeat || !this.loggedIn || !ownsTitle(this.$route.path)) {
                return;
            }
            const target = e.target;
            const editable =
                target instanceof HTMLElement &&
                (target.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName));
            const action = this.shortcutMatcher.match({
                key: e.key,
                ctrlKey: e.ctrlKey,
                metaKey: e.metaKey,
                altKey: e.altKey,
                editable,
                time: Date.now(),
            });
            if (action === null) {
                return;
            }
            // A dialog that's open has the keys, but the palette's own shortcut closes it
            const dialogOpen = document.querySelector(".modal.show");
            if (dialogOpen && !(action === "palette" && dialogOpen.matches("[data-command-palette]"))) {
                return;
            }
            if (this.runShortcut(action)) {
                e.preventDefault();
            }
        },

        /**
         * Do what a shortcut does
         * @param {string} action The shortcut
         * @returns {boolean} Whether it did something
         */
        runShortcut(action) {
            const go = (path) => {
                if (this.$route.path !== path) {
                    this.$router.push(path);
                }
                return true;
            };
            const monitorPage = this.$route.path.match(/^\/dashboard\/(\d+)$/);

            switch (action) {
                case "palette":
                    this.emitter.emit("toggleCommandPalette");
                    return true;
                case "help":
                    this.emitter.emit("showShortcutsHelp");
                    return true;
                case "search":
                    if (this.isMobile && this.$route.path !== "/list") {
                        this.$router.push("/list").then(() => this.emitter.emit("focusMonitorSearch"));
                    } else {
                        this.emitter.emit("focusMonitorSearch");
                    }
                    return true;
                case "newMonitor":
                    return this.canEdit ? go("/add") : false;
                case "next":
                case "previous":
                    this.emitter.emit("monitorListMove", action === "next" ? 1 : -1);
                    return true;
                case "edit":
                    return this.canEdit && monitorPage ? go(`/edit/${monitorPage[1]}`) : false;
                case "goDashboard":
                    return go("/dashboard");
                case "goStatusPages":
                    return go("/manage-status-page");
                case "goMaintenance":
                    return go("/maintenance");
                case "goSettings":
                    return go(this.canEdit ? "/settings/general" : "/settings/security");
                default:
                    return false;
            }
        },
    },
};
