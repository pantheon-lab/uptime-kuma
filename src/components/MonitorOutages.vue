<template>
    <div class="shadow-box big-padding mb-3 outages" data-testid="monitor-outages">
        <div class="outages-header">
            <h2 class="h5 mb-0 me-auto">{{ $t("Outages") }}</h2>
            <select
                v-model.number="days"
                class="form-select form-select-sm w-auto"
                data-testid="outages-days"
                :aria-label="$t('eventFilterTimeRange')"
            >
                <option v-for="d in [7, 30, 90, 365]" :key="d" :value="d">{{ $t("outagesLastDays", { n: d }) }}</option>
            </select>
        </div>

        <p class="outages-summary mb-2" data-testid="outages-summary">
            <template v-if="loading && count === null">…</template>
            <template v-else-if="count === 0">{{ $t("outagesNone", { n: days }) }}</template>
            <template v-else>
                {{
                    $t(count === 1 ? "outagesSummaryOne" : "outagesSummary", {
                        count,
                        n: days,
                        downtime: formatDuration(downtimeSeconds),
                    })
                }}
            </template>
        </p>

        <ol v-if="outages.length" class="outage-list">
            <li v-for="o in outages" :key="o.start" class="outage" data-testid="outage">
                <span class="when">
                    <Datetime :value="o.start" />
                </span>
                <span class="duration">
                    <span v-if="o.ongoing" class="badge bg-danger">{{ $t("outageOngoing") }}</span>
                    {{ formatDuration(o.durationSeconds) }}
                </span>
                <span class="msg" :title="o.msg">{{ o.msg }}</span>
                <button
                    type="button"
                    class="btn btn-sm btn-outline-normal"
                    data-testid="outage-show"
                    :title="$t('outageShowOnChartHelp')"
                    @click="showOnChart(o)"
                >
                    <font-awesome-icon icon="chart-line" />
                    <span class="visually-hidden">{{ $t("outageShowOnChartHelp") }}</span>
                </button>
            </li>
        </ol>
        <p v-if="count > outages.length" class="form-text mb-0">
            {{ $t("outagesMore", { shown: outages.length }) }}
        </p>
    </div>
</template>

<script>
import Datetime from "./Datetime.vue";
import { outageChartRange } from "../outage-range";
import { timeDurationFormatter } from "../util-frontend";

/**
 * The monitor's outages: from when it went down until it came back, newest first.
 */
export default {
    components: { Datetime },
    props: {
        /** The monitor */
        monitorId: {
            type: Number,
            required: true,
        },
    },
    emits: ["show-range"],
    data() {
        return {
            days: 30,
            loading: false,
            outages: [],
            count: null,
            downtimeSeconds: 0,
        };
    },
    watch: {
        days() {
            this.load();
        },
    },
    mounted() {
        this.load();
        this.$root.emitter.on("newImportantHeartbeat", this.onImportantBeat);
    },
    beforeUnmount() {
        this.$root.emitter.off("newImportantHeartbeat", this.onImportantBeat);
        clearTimeout(this.reloadTimer);
    },
    methods: {
        /**
         * Load the outages of the window chosen
         * @returns {void}
         */
        load() {
            this.loading = true;
            this.$root.getSocket().emit("getMonitorOutages", this.monitorId, this.days, (res) => {
                this.loading = false;
                if (res.ok) {
                    this.outages = res.outages;
                    this.count = res.count;
                    this.downtimeSeconds = res.downtimeSeconds;
                } else {
                    this.$root.toastError(res.msg);
                }
            });
        },

        /**
         * A change of this monitor's status starts or ends an outage: load them again, once
         * @param {object} beat The beat
         * @returns {void}
         */
        onImportantBeat(beat) {
            if (beat.monitorID === this.monitorId) {
                clearTimeout(this.reloadTimer);
                this.reloadTimer = setTimeout(() => this.load(), 500);
            }
        },

        /**
         * @param {number} seconds Seconds
         * @returns {string} Such as "1 hr, 5 min"
         */
        formatDuration(seconds) {
            if (seconds < 60) {
                return this.$t("outageUnderMinute");
            }
            // Whole minutes are enough here
            return timeDurationFormatter.secondsToHumanReadableFormat(seconds - (seconds % 60));
        },

        /**
         * Show an outage, with some time either side, on the chart
         * @param {object} o The outage
         * @returns {void}
         */
        showOnChart(o) {
            const { from, to } = outageChartRange(o, new Date());
            this.$emit("show-range", from, to);
        },
    },
};
</script>

<style lang="scss" scoped>
.outages-header {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-bottom: 8px;
}

.outage-list {
    list-style: none;
    margin: 0;
    padding: 0;
}

.outage {
    display: grid;
    grid-template-columns: 11rem 9rem minmax(0, 1fr) auto;
    align-items: center;
    gap: 12px;
    padding: 6px 0;
    border-top: 1px solid rgba(128, 128, 128, 0.15);
    font-size: 14px;

    .msg {
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        opacity: 0.8;
    }

    @media (max-width: 576px) {
        grid-template-columns: 1fr auto;

        .msg {
            grid-column: 1 / -1;
        }
    }
}
</style>
