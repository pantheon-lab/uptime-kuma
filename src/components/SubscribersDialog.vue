<template>
    <div
        ref="modal"
        class="modal fade"
        tabindex="-1"
        aria-labelledby="subscribers-title"
        data-testid="subscribers-dialog"
    >
        <div class="modal-dialog modal-lg modal-dialog-scrollable">
            <div class="modal-content">
                <div class="modal-header">
                    <h5 id="subscribers-title" class="modal-title">{{ $t("subscribersTitle") }}</h5>
                    <button type="button" class="btn-close" data-bs-dismiss="modal" :aria-label="$t('Close')" />
                </div>
                <div class="modal-body">
                    <p class="form-text mt-0">{{ $t("subscribersHelp") }}</p>

                    <h6>{{ $t("subscribersEmail") }}</h6>
                    <div class="mb-1">
                        <label for="subscribe-sender" class="form-label">{{ $t("subscribersSender") }}</label>
                        <select
                            id="subscribe-sender"
                            class="form-select"
                            :value="notificationId ?? ''"
                            :disabled="saving"
                            data-testid="subscribers-sender"
                            @change="setSender($event.target.value)"
                        >
                            <option value="">{{ $t("subscribersEmailOff") }}</option>
                            <option v-for="n in smtpNotifications" :key="n.id" :value="String(n.id)">
                                {{ n.name }}
                            </option>
                        </select>
                    </div>
                    <div class="form-text mb-3" data-testid="subscribers-sender-help">
                        <template v-if="smtpNotifications.length === 0">
                            {{ $t("subscribersNoSMTP") }}
                            <router-link to="/settings/notifications">{{ $t("Settings") }}</router-link>
                        </template>
                        <template v-else-if="!baseURL">
                            {{ $t("subscribersNoBaseURL") }}
                            <router-link to="/settings/general">{{ $t("Settings") }}</router-link>
                        </template>
                        <template v-else-if="notificationId">{{ $t("subscribersEmailOn") }}</template>
                        <template v-else>{{ $t("subscribersEmailOffHelp") }}</template>
                    </div>

                    <h6>{{ $t("subscribersWebhooks") }}</h6>
                    <form class="input-group mb-1" @submit.prevent="addWebhook">
                        <label for="subscribe-webhook" class="visually-hidden">{{ $t("subscribersWebhookURL") }}</label>
                        <input
                            id="subscribe-webhook"
                            v-model="webhookURL"
                            type="url"
                            class="form-control"
                            required
                            placeholder="https://example.com/hooks/status"
                            data-testid="subscribers-webhook-url"
                        />
                        <button type="submit" class="btn btn-outline-normal" :disabled="saving">
                            <font-awesome-icon icon="plus" />
                            {{ $t("Add") }}
                        </button>
                    </form>
                    <div class="form-text mb-3">{{ $t("subscribersWebhookHelp") }}</div>

                    <h6 class="d-flex align-items-baseline">
                        <span class="me-auto">{{ $t("subscribersList") }}</span>
                        <span class="form-text" data-testid="subscribers-count">
                            {{ $t("subscribersCount", [confirmedCount, waitingCount]) }}
                        </span>
                    </h6>
                    <p v-if="subscribers.length === 0" class="form-text">{{ $t("subscribersNone") }}</p>
                    <ul v-else class="list-group subscriber-list">
                        <li
                            v-for="sub in subscribers"
                            :key="sub.id"
                            class="list-group-item d-flex align-items-center gap-2"
                            data-testid="subscriber"
                        >
                            <font-awesome-icon
                                :icon="sub.kind === 'email' ? 'envelope' : 'link'"
                                class="flex-shrink-0"
                                :aria-label="sub.kind === 'email' ? $t('Email') : $t('subscribersWebhook')"
                            />
                            <div class="flex-grow-1 text-break">
                                <div>{{ sub.address }}</div>
                                <div class="form-text mt-0">
                                    <span v-if="!sub.confirmed" class="badge bg-secondary me-1">
                                        {{ $t("subscribersWaiting") }}
                                    </span>
                                    <span v-if="sub.lastError" class="text-danger" data-testid="subscriber-error">
                                        {{ $t("subscribersLastError", [sub.lastError]) }}
                                    </span>
                                    <span v-else-if="sub.lastSentDate && sub.confirmed">
                                        {{ $t("subscribersLastSent", [fromNow(sub.lastSentDate)]) }}
                                    </span>
                                </div>
                            </div>
                            <button
                                type="button"
                                class="btn btn-sm btn-outline-danger"
                                :aria-label="$t('subscribersRemove', [sub.address])"
                                data-testid="subscriber-remove"
                                @click="remove(sub)"
                            >
                                <font-awesome-icon icon="trash" />
                            </button>
                        </li>
                    </ul>
                </div>
                <div class="modal-footer">
                    <button type="button" class="btn btn-secondary" data-bs-dismiss="modal">{{ $t("Close") }}</button>
                </div>
            </div>
        </div>
    </div>
</template>

<script>
import { Modal } from "bootstrap";
import dayjs from "dayjs";
import { smtpNotifications } from "../status-subscriptions";

/**
 * A status page's subscribers: the notification its emails go through, its webhooks, and who's
 * subscribed.
 */
export default {
    props: {
        slug: {
            type: String,
            required: true,
        },
    },
    emits: ["changed"],
    data() {
        return {
            modal: null,
            subscribers: [],
            notificationId: null,
            baseURL: "",
            webhookURL: "",
            saving: false,
        };
    },
    computed: {
        smtpNotifications() {
            return smtpNotifications(this.$root.notificationList);
        },
        confirmedCount() {
            return this.subscribers.filter((s) => s.confirmed).length;
        },
        waitingCount() {
            return this.subscribers.length - this.confirmedCount;
        },
    },
    mounted() {
        this.modal = new Modal(this.$refs.modal);
        this.load();
    },
    beforeUnmount() {
        this.modal?.dispose();
    },
    methods: {
        /**
         * Open the dialog, with the list as it is now
         * @returns {void}
         */
        show() {
            this.load();
            this.modal.show();
        },

        /**
         * Read the subscribers, and tell the page how many are confirmed
         * @returns {void}
         */
        load() {
            this.$root.getSocket().emit("getStatusPageSubscribers", this.slug, (res) => {
                if (!res.ok) {
                    this.$root.toastRes(res);
                    return;
                }
                this.subscribers = res.subscribers;
                this.notificationId = res.notificationId;
                this.baseURL = res.baseURL;
                this.$emit("changed", { confirmed: this.confirmedCount, email: !!res.notificationId });
            });
        },

        /**
         * Set the notification the page's emails go through
         * @param {string} value Its id, or "" for none
         * @returns {void}
         */
        setSender(value) {
            this.saving = true;
            const notificationId = value === "" ? null : Number(value);
            this.$root.getSocket().emit("setStatusPageSubscriptions", this.slug, { notificationId }, (res) => {
                this.saving = false;
                this.$root.toastRes(res);
                this.load();
            });
        },

        /**
         * Add the webhook in the field
         * @returns {void}
         */
        addWebhook() {
            this.saving = true;
            this.$root.getSocket().emit("addStatusPageWebhook", this.slug, this.webhookURL, (res) => {
                this.saving = false;
                this.$root.toastRes(res);
                if (res.ok) {
                    this.webhookURL = "";
                    this.load();
                }
            });
        },

        /**
         * Remove a subscriber
         * @param {object} sub The subscriber
         * @returns {void}
         */
        remove(sub) {
            this.$root.getSocket().emit("removeStatusPageSubscriber", this.slug, sub.id, (res) => {
                this.$root.toastRes(res);
                this.load();
            });
        },

        /**
         * How long ago
         * @param {string} date UTC date from the server
         * @returns {string} Such as "5 minutes ago"
         */
        fromNow(date) {
            return dayjs.utc(date).fromNow();
        },
    },
};
</script>

<style lang="scss" scoped>
.subscriber-list {
    max-height: 40vh;
    overflow-y: auto;
}
</style>
