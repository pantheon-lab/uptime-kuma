import { pageTitle } from "../page-title";
import { appName } from "../util.ts";

/**
 * Keeps the browser tab's title on the page shown, with the number of monitors down first.
 */
export default {
    computed: {
        appPageTitle() {
            return pageTitle(
                {
                    path: this.$route.path,
                    login: !this.loggedIn && this.allowLoginDialog,
                    down: this.loggedIn ? this.stats.down : 0,
                    monitorList: this.monitorList,
                },
                (key) => this.$t(key),
                appName
            );
        },
    },

    watch: {
        appPageTitle: {
            handler(title) {
                if (title != null) {
                    document.title = title;
                }
            },
            immediate: true,
        },
    },
};
