/**
 * Asks before leaving a form with changes that aren't saved, in the app and when the tab is closed.
 *
 * The component defines unsavedState(), the values the form saves, and calls takeUnsavedSnapshot()
 * once they're loaded or saved, and forgetUnsavedChanges() before leaving after a save.
 */
export default {
    data() {
        return {
            unsavedSnapshot: null,
        };
    },

    mounted() {
        window.addEventListener("beforeunload", this.onUnsavedBeforeUnload);
        this.removeUnsavedGuard = this.$router.beforeEach((to, from) => {
            if (to.fullPath === from.fullPath || !this.hasUnsavedChanges()) {
                return true;
            }
            if (window.confirm(this.$t("unsavedChangesLeave"))) {
                this.forgetUnsavedChanges();
                return true;
            }
            return false;
        });
    },

    beforeUnmount() {
        window.removeEventListener("beforeunload", this.onUnsavedBeforeUnload);
        this.removeUnsavedGuard?.();
        clearTimeout(this.unsavedSnapshotTimer);
    },

    methods: {
        /**
         * Records the form as it is now, as having nothing to save. Waits for the watchers and the
         * fields that adjust what they're given, so that they don't count as changes.
         * @returns {void}
         */
        takeUnsavedSnapshot() {
            clearTimeout(this.unsavedSnapshotTimer);
            this.$nextTick(() => {
                this.unsavedSnapshotTimer = setTimeout(() => {
                    this.unsavedSnapshot = this.serializeUnsavedState();
                }, 0);
            });
        },

        /**
         * Stops asking, such as after a save that leaves the page
         * @returns {void}
         */
        forgetUnsavedChanges() {
            clearTimeout(this.unsavedSnapshotTimer);
            this.unsavedSnapshot = null;
        },

        /**
         * @returns {boolean} Whether the form differs from what was loaded or saved
         */
        hasUnsavedChanges() {
            return this.unsavedSnapshot !== null && this.serializeUnsavedState() !== this.unsavedSnapshot;
        },

        /**
         * @returns {string} The form's state as JSON
         */
        serializeUnsavedState() {
            try {
                return JSON.stringify(this.unsavedState());
            } catch {
                return "";
            }
        },

        /**
         * @param {BeforeUnloadEvent} e The event
         * @returns {void}
         */
        onUnsavedBeforeUnload(e) {
            if (this.hasUnsavedChanges()) {
                e.preventDefault();
                e.returnValue = "";
            }
        },
    },
};
