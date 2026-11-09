<template>
    <div ref="modal" class="modal fade" tabindex="-1" aria-labelledby="bulk-edit-title" data-testid="bulk-edit-dialog">
        <div class="modal-dialog modal-lg modal-dialog-scrollable">
            <form class="modal-content" @submit.prevent="save">
                <div class="modal-header">
                    <h5 id="bulk-edit-title" class="modal-title">
                        {{ $t("bulkEditTitle", ids.length) }}
                    </h5>
                    <button type="button" class="btn-close" data-bs-dismiss="modal" :aria-label="$t('Close')" />
                </div>
                <!-- Only while open: its labels repeat the names of notifications in the page -->
                <div v-if="open" class="modal-body">
                    <p class="form-text mt-0">{{ $t("bulkEditHelp") }}</p>

                    <h6>{{ $t("General") }}</h6>
                    <div class="row g-2 mb-3">
                        <div class="col-sm-4">
                            <label for="bulk-interval" class="form-label">{{ $t("Heartbeat Interval") }}</label>
                            <input
                                id="bulk-interval"
                                v-model="interval"
                                type="number"
                                class="form-control"
                                min="1"
                                step="1"
                                :placeholder="$t('bulkEditUnchanged')"
                                data-testid="bulk-interval"
                            />
                        </div>
                        <div class="col-sm-4">
                            <label for="bulk-retry-interval" class="form-label">
                                {{ $t("Heartbeat Retry Interval") }}
                            </label>
                            <input
                                id="bulk-retry-interval"
                                v-model="retryInterval"
                                type="number"
                                class="form-control"
                                min="0"
                                step="1"
                                :placeholder="$t('bulkEditUnchanged')"
                                data-testid="bulk-retry-interval"
                            />
                        </div>
                        <div class="col-sm-4">
                            <label for="bulk-max-retries" class="form-label">{{ $t("Retries") }}</label>
                            <input
                                id="bulk-max-retries"
                                v-model="maxRetries"
                                type="number"
                                class="form-control"
                                min="0"
                                step="1"
                                :placeholder="$t('bulkEditUnchanged')"
                                data-testid="bulk-max-retries"
                            />
                        </div>
                    </div>

                    <h6>{{ $t("Notifications") }}</h6>
                    <p v-if="notifications.length === 0" class="form-text">{{ $t("Not available, please setup.") }}</p>
                    <div
                        v-for="n in notifications"
                        :key="n.id"
                        class="change-row"
                        data-testid="bulk-notification"
                        :data-name="n.name"
                    >
                        <span class="change-name">{{ n.name }}</span>
                        <span class="form-text me-2">{{ $t("bulkEditHave", [have.notifications[n.id] ?? 0]) }}</span>
                        <select
                            v-model="notificationChanges[n.id]"
                            class="form-select form-select-sm w-auto"
                            :aria-label="n.name"
                        >
                            <option value="">{{ $t("bulkEditLeave") }}</option>
                            <option value="add">{{ $t("bulkEditAdd") }}</option>
                            <option value="remove">{{ $t("bulkEditRemove") }}</option>
                        </select>
                    </div>

                    <h6 class="mt-3">{{ $t("Tags") }}</h6>
                    <p v-if="tags.length === 0" class="form-text">{{ $t("bulkEditNoTags") }}</p>
                    <div
                        v-for="tag in tags"
                        :key="tag.id"
                        class="change-row"
                        data-testid="bulk-tag"
                        :data-name="tag.name"
                    >
                        <span class="change-name">
                            <span class="tag-dot" :style="{ backgroundColor: tag.color }"></span>
                            {{ tag.name }}
                        </span>
                        <span class="form-text me-2">{{ $t("bulkEditHave", [have.tags[tag.id] ?? 0]) }}</span>
                        <input
                            v-if="tagChanges[tag.id] === 'add'"
                            v-model="tagValues[tag.id]"
                            type="text"
                            class="form-control form-control-sm tag-value"
                            maxlength="200"
                            :placeholder="$t('conditionValuePlaceholder')"
                            :aria-label="$t('conditionValuePlaceholder')"
                        />
                        <select
                            v-model="tagChanges[tag.id]"
                            class="form-select form-select-sm w-auto"
                            :aria-label="tag.name"
                        >
                            <option value="">{{ $t("bulkEditLeave") }}</option>
                            <option value="add">{{ $t("bulkEditAdd") }}</option>
                            <option value="remove">{{ $t("bulkEditRemoveTag") }}</option>
                        </select>
                    </div>
                </div>
                <div class="modal-footer">
                    <span class="me-auto form-text" data-testid="bulk-edit-summary">{{ summary }}</span>
                    <button type="button" class="btn btn-outline-normal" data-bs-dismiss="modal">
                        {{ $t("Cancel") }}
                    </button>
                    <button
                        type="submit"
                        class="btn btn-primary"
                        :disabled="processing || !hasChanges"
                        data-testid="bulk-edit-save"
                    >
                        {{ $t("bulkEditSave", ids.length) }}
                    </button>
                </div>
            </form>
        </div>
    </div>
</template>

<script>
import { Modal } from "bootstrap";

/**
 * Changes the interval, retries, notifications and tags of the selected monitors at once.
 */
export default {
    emits: ["saved"],
    data() {
        return {
            modal: null,
            ids: [],
            interval: "",
            retryInterval: "",
            maxRetries: "",
            notificationChanges: {},
            tagChanges: {},
            tagValues: {},
            tags: [],
            // How many of the selected monitors have each notification and tag
            have: { notifications: {}, tags: {} },
            processing: false,
            open: false,
        };
    },
    computed: {
        notifications() {
            return [...this.$root.notificationList].sort((a, b) => a.name.localeCompare(b.name));
        },

        /**
         * The changes, as the server takes them
         * @returns {object} Changes
         */
        changes() {
            const number = (v) => (v === "" || v === null ? undefined : Number(v));
            const pick = (map, want) =>
                Object.entries(map)
                    .filter(([, c]) => c === want)
                    .map(([id]) => Number(id));
            return {
                interval: number(this.interval),
                retryInterval: number(this.retryInterval),
                maxRetries: number(this.maxRetries),
                addNotifications: pick(this.notificationChanges, "add"),
                removeNotifications: pick(this.notificationChanges, "remove"),
                addTags: pick(this.tagChanges, "add").map((id) => ({
                    tagId: id,
                    value: (this.tagValues[id] ?? "").trim(),
                })),
                removeTags: pick(this.tagChanges, "remove"),
            };
        },

        hasChanges() {
            const c = this.changes;
            return (
                c.interval !== undefined ||
                c.retryInterval !== undefined ||
                c.maxRetries !== undefined ||
                c.addNotifications.length + c.removeNotifications.length + c.addTags.length + c.removeTags.length > 0
            );
        },

        /**
         * What saving will do, in a few words
         * @returns {string} Summary
         */
        summary() {
            if (!this.hasChanges) {
                return this.$t("bulkEditNothing");
            }
            const c = this.changes;
            const parts = [];
            if (c.interval !== undefined) {
                parts.push(this.$t("bulkEditSummaryInterval", [c.interval]));
            }
            if (c.retryInterval !== undefined) {
                parts.push(this.$t("bulkEditSummaryRetryInterval", [c.retryInterval]));
            }
            if (c.maxRetries !== undefined) {
                parts.push(this.$t("bulkEditSummaryRetries", [c.maxRetries]));
            }
            const names = (ids, list) => ids.map((id) => list.find((x) => x.id === id)?.name ?? id).join(", ");
            if (c.addNotifications.length) {
                parts.push(this.$t("bulkEditSummaryAdd", [names(c.addNotifications, this.notifications)]));
            }
            if (c.removeNotifications.length) {
                parts.push(this.$t("bulkEditSummaryRemove", [names(c.removeNotifications, this.notifications)]));
            }
            if (c.addTags.length) {
                parts.push(
                    this.$t("bulkEditSummaryAdd", [
                        names(
                            c.addTags.map((t) => t.tagId),
                            this.tags
                        ),
                    ])
                );
            }
            if (c.removeTags.length) {
                parts.push(this.$t("bulkEditSummaryRemove", [names(c.removeTags, this.tags)]));
            }
            return parts.join("; ");
        },
    },
    mounted() {
        this.modal = new Modal(this.$refs.modal);
        this.$refs.modal.addEventListener("hidden.bs.modal", this.onHidden);
    },
    beforeUnmount() {
        this.$refs.modal?.removeEventListener("hidden.bs.modal", this.onHidden);
        this.modal?.hide();
        this.modal?.dispose();
    },
    methods: {
        /**
         * Show the dialog for these monitors, with nothing changed yet
         * @param {Array<number|string>} ids The selected monitors
         * @returns {void}
         */
        show(ids) {
            this.ids = ids.map(Number);
            this.interval = "";
            this.retryInterval = "";
            this.maxRetries = "";
            this.notificationChanges = Object.fromEntries(this.$root.notificationList.map((n) => [n.id, ""]));
            this.tagChanges = {};
            this.tagValues = {};
            this.countHave();
            this.open = true;
            this.$root.getSocket().emit("getTags", (res) => {
                if (res.ok) {
                    this.tags = [...res.tags].sort((a, b) => a.name.localeCompare(b.name));
                    this.tagChanges = Object.fromEntries(this.tags.map((t) => [t.id, ""]));
                }
            });
            this.modal.show();
        },

        /**
         * The dialog closed
         * @returns {void}
         */
        onHidden() {
            this.open = false;
        },

        /**
         * Count how many of the selected monitors already have each notification and tag
         * @returns {void}
         */
        countHave() {
            const notifications = {};
            const tags = {};
            for (const id of this.ids) {
                const m = this.$root.monitorList[id];
                if (!m) {
                    continue;
                }
                for (const [nid, on] of Object.entries(m.notificationIDList ?? {})) {
                    if (on) {
                        notifications[nid] = (notifications[nid] ?? 0) + 1;
                    }
                }
                for (const tid of new Set((m.tags ?? []).map((t) => t.tag_id))) {
                    tags[tid] = (tags[tid] ?? 0) + 1;
                }
            }
            this.have = { notifications, tags };
        },

        /**
         * Send the changes
         * @returns {void}
         */
        save() {
            if (!this.hasChanges || this.processing) {
                return;
            }
            this.processing = true;
            this.$root.getSocket().emit("bulkEditMonitors", this.ids, this.changes, (res) => {
                this.processing = false;
                this.$root.toastRes(res);
                if (res.ok) {
                    this.modal.hide();
                    this.$emit("saved");
                }
            });
        },
    },
};
</script>

<style lang="scss" scoped>
h6 {
    font-weight: 600;
    margin-bottom: 8px;
}

.change-row {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 4px 0;
    border-top: 1px solid rgba(128, 128, 128, 0.15);

    .change-name {
        margin-right: auto;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
}

.tag-dot {
    display: inline-block;
    width: 10px;
    height: 10px;
    border-radius: 50%;
    margin-right: 4px;
}

.tag-value {
    max-width: 10rem;
}
</style>
