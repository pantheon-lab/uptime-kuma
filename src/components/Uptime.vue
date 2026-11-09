<template>
    <span :class="className" :title="title" :data-status="pill ? lastHeartBeat.status : null">
        <!-- In a pill, the colour is the status: an icon and words say it too -->
        <template v-if="pill && statusIcon">
            <font-awesome-icon :icon="statusIcon" class="status-icon" aria-hidden="true" />
        </template>
        <span v-if="pill && statusName" class="visually-hidden">{{ statusName }}:</span>
        {{ uptime }}
    </span>
</template>

<script>
import { DOWN, MAINTENANCE, PENDING, UP } from "../util.ts";

export default {
    props: {
        /** Monitor this represents */
        monitor: {
            type: Object,
            default: null,
        },
        /** Type of monitor */
        type: {
            type: String,
            default: null,
        },
        /** Is this a pill? */
        pill: {
            type: Boolean,
            default: false,
        },
    },

    computed: {
        uptime() {
            if (this.type === "maintenance") {
                return this.$t("statusMaintenance");
            }

            let key = this.monitor.id + "_" + this.type;

            if (this.$root.uptimeList[key] !== undefined) {
                let result = Math.round(this.$root.uptimeList[key] * 10000) / 100;
                // Only perform sanity check on status page. See louislam/uptime-kuma#2628
                if (this.$route.path.startsWith("/status") && result > 100) {
                    return "100%";
                } else {
                    return result + "%";
                }
            }

            return this.$t("notAvailableShort");
        },

        color() {
            if (this.lastHeartBeat.status === MAINTENANCE) {
                return "maintenance";
            }

            if (this.lastHeartBeat.status === DOWN) {
                return "danger";
            }

            if (this.lastHeartBeat.status === UP) {
                return "primary";
            }

            if (this.lastHeartBeat.status === PENDING) {
                return "warning";
            }

            return "secondary";
        },

        /**
         * The icon that says the status, beside its colour. Up needs none.
         * @returns {string|null} Icon
         */
        statusIcon() {
            return (
                {
                    [DOWN]: "times-circle",
                    [PENDING]: "spinner",
                    [MAINTENANCE]: "wrench",
                }[this.lastHeartBeat.status] ?? null
            );
        },

        /**
         * The status in words, for screen readers
         * @returns {string|null} Status
         */
        statusName() {
            return (
                {
                    [UP]: this.$t("Up"),
                    [DOWN]: this.$t("Down"),
                    [PENDING]: this.$t("Pending"),
                    [MAINTENANCE]: this.$t("statusMaintenance"),
                }[this.lastHeartBeat.status] ?? null
            );
        },

        lastHeartBeat() {
            if (this.monitor.id in this.$root.lastHeartbeatList && this.$root.lastHeartbeatList[this.monitor.id]) {
                return this.$root.lastHeartbeatList[this.monitor.id];
            }

            return {
                status: -1,
            };
        },

        className() {
            if (this.pill) {
                return `badge rounded-pill bg-${this.color}`;
            }

            return "";
        },

        title() {
            if (this.type === "1y") {
                return this.$t("years", 1);
            }
            if (this.type === "720") {
                return this.$t("days", 30);
            }
            return this.$t("hours", 24);
        },
    },
};
</script>

<style>
.badge {
    min-width: 62px;
}

.badge .status-icon {
    margin-right: 3px;
}
</style>
