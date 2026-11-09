<template>
    <transition ref="tableContainer" name="slide-fade" appear>
        <div v-if="$route.name === 'DashboardHome'">
            <GettingStarted />
            <div
                v-if="!$root.canEdit && $root.monitorListLoaded && Object.keys($root.monitorList).length === 0"
                class="shadow-box big-padding mb-3"
                data-testid="viewer-no-monitors"
            >
                {{ $t("viewerNoMonitors") }}
            </div>
            <h1 class="mb-3">
                {{ $t("Quick Stats") }}
            </h1>

            <div class="shadow-box big-padding text-center mb-3 quick-stats" data-testid="quick-stats">
                <div class="row quick-stats-row">
                    <div v-for="stat in quickStats" :key="stat.key" class="col">
                        <button
                            v-if="stat.filter"
                            type="button"
                            class="stat"
                            :class="{ active: isStatFiltered(stat) }"
                            :data-testid="`quick-stat-${stat.key}`"
                            :aria-pressed="isStatFiltered(stat)"
                            :title="$t('quickStatFilter', [stat.label])"
                            @click="filterByStat(stat)"
                        >
                            <span class="h3 d-block">{{ stat.label }}</span>
                            <span class="num" :class="stat.numClass">{{ stat.count }}</span>
                        </button>
                        <div v-else class="stat" :data-testid="`quick-stat-${stat.key}`">
                            <h3>{{ stat.label }}</h3>
                            <span class="num" :class="stat.numClass">{{ stat.count }}</span>
                        </div>
                    </div>
                </div>
            </div>

            <div
                v-if="downMonitorIDs.length > 0"
                class="shadow-box down-banner mb-3 d-flex flex-wrap align-items-center gap-2"
                data-testid="down-banner"
            >
                <font-awesome-icon icon="exclamation-circle" class="text-danger" />
                <span class="me-auto">{{ $t("monitorsDownBanner", downMonitorIDs.length) }}</span>
                <button
                    type="button"
                    class="btn btn-sm btn-outline-danger"
                    data-testid="ai-prompt-all"
                    :title="$t('aiPromptButtonHelp')"
                    @click="$refs.aiPrompt.show(downMonitorIDs)"
                >
                    <font-awesome-icon icon="robot" />
                    {{ $t("aiPromptButton") }}
                </button>
            </div>

            <div class="shadow-box table-shadow-box table-wrapper">
                <div class="events-toolbar mb-3">
                    <h2 class="h5 mb-0 me-auto">{{ $t("Events") }}</h2>
                    <select
                        v-model="eventFilter.status"
                        class="form-select form-select-sm"
                        data-testid="event-filter-status"
                        :aria-label="$t('Status')"
                    >
                        <option value="">{{ $t("eventFilterAllStatuses") }}</option>
                        <option v-for="option in statusOptions" :key="option.value" :value="option.value">
                            {{ option.label }}
                        </option>
                    </select>
                    <select
                        v-model="eventFilter.monitorID"
                        class="form-select form-select-sm monitor-select"
                        data-testid="event-filter-monitor"
                        :aria-label="$t('eventFilterMonitor')"
                    >
                        <option value="">{{ $t("eventFilterAllMonitors") }}</option>
                        <option v-for="option in monitorOptions" :key="option.id" :value="String(option.id)">
                            {{ option.label }}
                        </option>
                    </select>
                    <select
                        v-model="eventFilter.range"
                        class="form-select form-select-sm"
                        data-testid="event-filter-range"
                        :aria-label="$t('eventFilterTimeRange')"
                    >
                        <option v-for="option in rangeOptions" :key="option.value" :value="option.value">
                            {{ option.label }}
                        </option>
                    </select>
                    <button
                        v-if="eventFilterActive"
                        type="button"
                        class="btn btn-sm btn-link"
                        data-testid="event-filter-clear"
                        @click="clearEventFilter"
                    >
                        {{ $t("eventFilterClear") }}
                    </button>
                    <div v-if="$root.canEdit" class="dropdown">
                        <button
                            type="button"
                            class="btn btn-sm btn-outline-normal"
                            data-bs-toggle="dropdown"
                            data-testid="events-menu"
                            aria-expanded="false"
                            :aria-label="$t('eventsMoreActions')"
                            :title="$t('eventsMoreActions')"
                        >
                            <font-awesome-icon icon="ellipsis-h" />
                        </button>
                        <ul class="dropdown-menu dropdown-menu-end">
                            <li>
                                <button
                                    type="button"
                                    class="dropdown-item text-danger"
                                    data-testid="clear-all-events"
                                    :disabled="clearingAllEvents"
                                    @click="clearAllEventsDialog"
                                >
                                    <font-awesome-icon icon="trash" class="me-2" />
                                    {{ $t("Clear All Events") }}
                                </button>
                            </li>
                        </ul>
                    </div>
                </div>
                <table class="table table-borderless table-hover">
                    <thead>
                        <tr>
                            <th v-if="showGroupColumn">{{ $t("Group Name") }}</th>
                            <th class="name-column">{{ $t("Name") }}</th>
                            <th>{{ $t("Status") }}</th>
                            <th>{{ $t("DateTime") }}</th>
                            <th>{{ $t("Message") }}</th>
                        </tr>
                    </thead>
                    <tbody>
                        <tr
                            v-for="(beat, index) in displayedRecords"
                            :key="index"
                            :class="{ 'shadow-box': $root.windowWidth <= 550 }"
                        >
                            <td v-if="showGroupColumn">
                                <router-link
                                    v-if="getGroupName(beat.monitorID)"
                                    :to="`/dashboard/${getGroupId(beat.monitorID)}`"
                                >
                                    {{ getGroupName(beat.monitorID) }}
                                </router-link>
                                <span v-else class="text-secondary">—</span>
                            </td>
                            <td class="name-column">
                                <router-link :to="`/dashboard/${beat.monitorID}`">
                                    {{ $root.monitorList[beat.monitorID]?.name }}
                                </router-link>
                            </td>
                            <td><Status :status="beat.status" /></td>
                            <td :class="{ 'border-0': !beat.msg }"><Datetime :value="beat.time" /></td>
                            <td class="border-0">{{ beat.msg }}</td>
                        </tr>

                        <tr v-if="importantHeartBeatListLength === 0">
                            <td :colspan="tableColumnCount" data-testid="events-empty">
                                {{ eventFilterActive ? $t("eventFilterNoMatch") : $t("No important events") }}
                            </td>
                        </tr>
                    </tbody>
                </table>

                <div class="d-flex justify-content-center kuma_pagination">
                    <pagination
                        v-model="page"
                        :records="importantHeartBeatListLength"
                        :per-page="perPage"
                        :options="paginationConfig"
                    />
                </div>
            </div>
        </div>
    </transition>
    <Confirm
        ref="confirmClearEvents"
        btn-style="btn-danger"
        :yes-text="$t('Yes')"
        :no-text="$t('No')"
        @yes="clearAllEvents"
    >
        {{ $t("clearAllEventsMsg") }}
    </Confirm>
    <!-- Only on the dashboard: a monitor's page, shown below, has its own -->
    <AIPromptDialog v-if="$route.name === 'DashboardHome'" ref="aiPrompt" />
    <router-view ref="child" />
</template>

<script>
import Status from "../components/Status.vue";
import Datetime from "../components/Datetime.vue";
import Pagination from "v-pagination-3";
import Confirm from "../components/Confirm.vue";
import AIPromptDialog from "../components/AIPromptDialog.vue";
import GettingStarted from "../components/GettingStarted.vue";
import { DOWN, MAINTENANCE, PENDING, UP } from "../util";

const EVENT_FILTER_KEY = "dashboardEventFilter";
const RANGE_HOURS = { "1h": 1, "24h": 24, "7d": 24 * 7, "30d": 24 * 30 };

/**
 * The events filter this browser last used
 * @returns {object} The filter
 */
function loadEventFilter() {
    const filter = { status: "", monitorID: "", range: "" };
    try {
        const saved = JSON.parse(localStorage.getItem(EVENT_FILTER_KEY) || "{}");
        for (const key of Object.keys(filter)) {
            if (typeof saved[key] === "string") {
                filter[key] = saved[key];
            }
        }
    } catch {
        // Storage may be blocked; start without a filter
    }
    return filter;
}

export default {
    components: {
        Datetime,
        Status,
        Pagination,
        Confirm,
        AIPromptDialog,
        GettingStarted,
    },
    props: {
        calculatedHeight: {
            type: Number,
            default: 0,
        },
    },
    data() {
        return {
            page: 1,
            perPage: 25,
            initialPerPage: 25,
            paginationConfig: {
                hideCount: true,
                chunksNavigation: "scroll",
            },
            importantHeartBeatListLength: 0,
            displayedRecords: [],
            clearingAllEvents: false,
            eventFilter: loadEventFilter(),
        };
    },
    computed: {
        showGroupColumn() {
            return Object.values(this.$root.monitorList).some((m) => m.parent != null);
        },
        tableColumnCount() {
            return this.showGroupColumn ? 5 : 4;
        },

        /**
         * The monitors down now, leaving out groups when a monitor in them is down too, as the
         * group is down because of it
         * @returns {number[]} Monitor ids
         */
        downMonitorIDs() {
            // At most once a second, as the counts are, not on every beat
            this.$root.heartbeatVersion;
            const raw = this.$root.lastBeatRaw;
            const down = Object.values(this.$root.monitorList).filter((m) => m.active && raw[m.id]?.status === DOWN);
            const downIDs = new Set(down.map((m) => m.id));
            return down
                .filter((m) => m.type !== "group" || !(m.childrenIDs ?? []).some((id) => downIDs.has(id)))
                .sort((a, b) => (a.pathName || a.name).localeCompare(b.pathName || b.name))
                .map((m) => m.id);
        },

        statusOptions() {
            return [
                { value: String(DOWN), label: this.$t("Down") },
                { value: String(UP), label: this.$t("Up") },
                { value: String(PENDING), label: this.$t("Pending") },
                { value: String(MAINTENANCE), label: this.$t("Maintenance") },
            ];
        },

        rangeOptions() {
            return [
                { value: "", label: this.$t("eventFilterAllTime") },
                { value: "1h", label: this.$t("eventFilterLastHour") },
                { value: "24h", label: this.$t("eventFilterLast24h") },
                { value: "7d", label: this.$t("eventFilterLast7d") },
                { value: "30d", label: this.$t("eventFilterLast30d") },
            ];
        },

        /**
         * Every monitor, by its path, such as "Group / API"
         * @returns {object[]} Options
         */
        monitorOptions() {
            return Object.values(this.$root.monitorList)
                .map((m) => ({ id: m.id, label: m.pathName || m.name }))
                .sort((a, b) => a.label.localeCompare(b.label));
        },

        eventFilterActive() {
            return this.eventFilter.status !== "" || this.eventFilter.monitorID !== "" || this.eventFilter.range !== "";
        },

        /**
         * The monitors whose events are shown: the one chosen and, for a group, everything in it
         * @returns {Set<number>|null} Monitor ids, or null for all
         */
        filteredMonitorIDs() {
            const id = Number(this.eventFilter.monitorID);
            if (!id) {
                return null;
            }
            const children = new Map();
            for (const m of Object.values(this.$root.monitorList)) {
                if (m.parent != null) {
                    if (!children.has(m.parent)) {
                        children.set(m.parent, []);
                    }
                    children.get(m.parent).push(m.id);
                }
            }
            const ids = new Set();
            const stack = [id];
            while (stack.length > 0) {
                const next = stack.pop();
                if (!ids.has(next)) {
                    ids.add(next);
                    stack.push(...(children.get(next) || []));
                }
            }
            return ids;
        },

        /**
         * The counts at the top, with the monitor list's filter each one applies
         * @returns {object[]} Counts
         */
        quickStats() {
            const stats = this.$root.stats;
            return [
                {
                    key: "up",
                    label: this.$t("Up"),
                    count: stats.up,
                    numClass: stats.up === 0 && "text-secondary",
                    filter: { status: [UP], active: null },
                },
                {
                    key: "down",
                    label: this.$t("Down"),
                    count: stats.down,
                    numClass: stats.down > 0 ? "text-danger" : "text-secondary",
                    filter: { status: [DOWN], active: null },
                },
                {
                    key: "maintenance",
                    label: this.$t("Maintenance"),
                    count: stats.maintenance,
                    numClass: stats.maintenance > 0 ? "text-maintenance" : "text-secondary",
                    filter: { status: [MAINTENANCE], active: null },
                },
                {
                    // Monitors without a beat yet, which the list's filter can't pick out
                    key: "unknown",
                    label: this.$t("Unknown"),
                    count: stats.unknown,
                    numClass: "text-secondary",
                    filter: null,
                },
                {
                    key: "pause",
                    label: this.$t("pauseDashboardHome"),
                    count: stats.pause,
                    numClass: "text-secondary",
                    filter: { status: null, active: [false] },
                },
            ];
        },
    },
    watch: {
        eventFilter: {
            handler() {
                try {
                    localStorage.setItem(EVENT_FILTER_KEY, JSON.stringify(this.eventFilter));
                } catch {
                    // Remembering the filter is a convenience
                }
                this.page = 1;
                this.getImportantHeartbeatListLength();
            },
            deep: true,
        },

        perPage() {
            this.$nextTick(() => {
                this.getImportantHeartbeatListPaged();
            });
        },

        page() {
            this.getImportantHeartbeatListPaged();
        },
    },

    mounted() {
        this.getImportantHeartbeatListLength();

        this.$root.emitter.on("newImportantHeartbeat", this.onNewImportantHeartbeat);

        this.initialPerPage = this.perPage;

        window.addEventListener("resize", this.updatePerPage);
        this.updatePerPage();
    },

    beforeUnmount() {
        this.$root.emitter.off("newImportantHeartbeat", this.onNewImportantHeartbeat);

        window.removeEventListener("resize", this.updatePerPage);
    },

    methods: {
        /**
         * Whether the monitor list shows only what this count counts
         * @param {object} stat A quick stat
         * @returns {boolean} Whether its filter is the list's
         */
        isStatFiltered(stat) {
            const current = this.$root.monitorListFilter;
            if (!current || !stat.filter) {
                return false;
            }
            const same = (a, b) => JSON.stringify(a?.length ? a : null) === JSON.stringify(b?.length ? b : null);
            return same(current.status, stat.filter.status) && same(current.active, stat.filter.active);
        },

        /**
         * Show only the monitors this count counts, or all of them again
         * @param {object} stat A quick stat
         * @returns {void}
         */
        filterByStat(stat) {
            this.$root.monitorListFilterRequest = this.isStatFiltered(stat)
                ? { status: null, active: null }
                : { ...stat.filter };
            // On phones the list is a page of its own
            if (this.$root.isMobile) {
                this.$router.push("/list");
            }
        },

        /**
         * Returns the group (parent) name for a monitor, or empty string if none.
         * @param {number} monitorID - The monitor ID.
         * @returns {string} The group name or empty string.
         */
        getGroupName(monitorID) {
            const monitor = this.$root.monitorList[monitorID];
            if (!monitor || monitor.parent == null) {
                return "";
            }
            const parent = this.$root.monitorList[monitor.parent];
            return parent ? parent.name : "";
        },

        /**
         * Returns the group (parent) ID for a monitor, or null if none.
         * @param {number} monitorID - The monitor ID.
         * @returns {number|null} The group monitor ID or null.
         */
        getGroupId(monitorID) {
            const monitor = this.$root.monitorList[monitorID];
            return monitor && monitor.parent != null ? monitor.parent : null;
        },

        /**
         * Updates the displayed records when a new important heartbeat arrives.
         * @param {object} heartbeat - The heartbeat object received.
         * @returns {void}
         */
        onNewImportantHeartbeat(heartbeat) {
            if (this.page === 1 && this.matchesEventFilter(heartbeat)) {
                this.displayedRecords.unshift(heartbeat);
                if (this.displayedRecords.length > this.perPage) {
                    this.displayedRecords.pop();
                }
                this.importantHeartBeatListLength += 1;
            }
        },

        /**
         * Retrieves the length of the important heartbeat list for all monitors.
         * @returns {void}
         */
        getImportantHeartbeatListLength() {
            this.$root.getSocket().emit("monitorImportantHeartbeatListCount", null, this.eventFilterArg(), (res) => {
                if (res.ok) {
                    this.importantHeartBeatListLength = res.count;
                    this.getImportantHeartbeatListPaged();
                }
            });
        },

        /**
         * Retrieves the important heartbeat list for the current page.
         * @returns {void}
         */
        getImportantHeartbeatListPaged() {
            const offset = (this.page - 1) * this.perPage;
            this.$root
                .getSocket()
                .emit(
                    "monitorImportantHeartbeatListPaged",
                    null,
                    offset,
                    this.perPage,
                    this.eventFilterArg(),
                    (res) => {
                        if (res.ok) {
                            this.displayedRecords = res.data;
                        }
                    }
                );
        },

        /**
         * Updates the number of items shown per page based on the available height.
         * @returns {void}
         */
        updatePerPage() {
            const tableContainer = this.$refs.tableContainer;
            const tableContainerHeight = tableContainer.offsetHeight;
            const availableHeight = window.innerHeight - tableContainerHeight;
            const additionalPerPage = Math.floor(availableHeight / 58);

            if (additionalPerPage > 0) {
                this.perPage = Math.max(this.initialPerPage, this.perPage + additionalPerPage);
            } else {
                this.perPage = this.initialPerPage;
            }
        },

        /**
         * The events filter as the server takes it
         * @returns {object|null} Filter, or null for none
         */
        eventFilterArg() {
            if (!this.eventFilterActive) {
                return null;
            }
            const filter = {};
            if (this.eventFilter.status !== "") {
                filter.statuses = [Number(this.eventFilter.status)];
            }
            if (this.filteredMonitorIDs) {
                filter.monitorIDs = [...this.filteredMonitorIDs];
            }
            const hours = RANGE_HOURS[this.eventFilter.range];
            if (hours) {
                filter.since = new Date(Date.now() - hours * 3600 * 1000).toISOString().replace(/\.\d+Z$/, "Z");
            }
            return filter;
        },

        /**
         * Whether a new event belongs in the list as it's filtered
         * @param {object} beat The event
         * @returns {boolean} Whether it does
         */
        matchesEventFilter(beat) {
            if (this.eventFilter.status !== "" && beat.status !== Number(this.eventFilter.status)) {
                return false;
            }
            return !this.filteredMonitorIDs || this.filteredMonitorIDs.has(beat.monitorID);
        },

        clearEventFilter() {
            this.eventFilter = { status: "", monitorID: "", range: "" };
        },

        clearAllEventsDialog() {
            this.$refs.confirmClearEvents.show();
        },
        clearAllEvents() {
            this.clearingAllEvents = true;
            this.$root.getSocket().emit("clearEvents", null, (res) => {
                this.clearingAllEvents = false;
                if (res?.ok) {
                    this.$root.toastSuccess(this.$t("Events cleared successfully"));
                } else {
                    this.$root.toastError(res?.msg || this.$t("Could not clear events", { failed: "?", total: "?" }));
                }
                this.page = 1;
                this.getImportantHeartbeatListLength();
            });
        },
    },
};
</script>

<style lang="scss" scoped>
@import "../assets/vars";

.num {
    font-size: 30px;
    color: $primary;
    font-weight: bold;
    display: block;
}

// Phones: the five counts in two short rows, rather than most of the first screen
@media (max-width: 576px) {
    .quick-stats.big-padding {
        padding: 10px;
    }

    .quick-stats-row {
        display: grid;
        grid-template-columns: repeat(3, minmax(0, 1fr));
        gap: 4px;
        margin: 0;

        > .col {
            padding: 0;
        }
    }

    .stat {
        padding: 4px 0;

        .h3,
        h3 {
            font-size: 14px;
            margin-bottom: 0;
            overflow: hidden;
            text-overflow: ellipsis;
            white-space: nowrap;
        }
    }

    .num {
        font-size: 22px;
    }
}

.stat {
    display: block;
    width: 100%;
    padding: 6px 0;
    border: 0;
    border-radius: 10px;
    background: none;
    color: inherit;
}

button.stat {
    cursor: pointer;
    transition: background-color ease-in-out 0.15s;

    &:hover,
    &:focus-visible {
        background-color: rgba($primary, 0.08);
    }

    &.active {
        background-color: rgba($primary, 0.15);
        box-shadow: inset 0 0 0 2px rgba($primary, 0.5);
    }
}

.shadow-box {
    padding: 20px;
}

table {
    font-size: 14px;

    tr {
        transition: all ease-in-out 0.2ms;
    }

    @media (max-width: 550px) {
        table-layout: fixed;
        overflow-wrap: break-word;
    }
}

@media screen and (max-width: 1280px) {
    .name-column {
        min-width: 150px;
    }
}

@media screen and (min-aspect-ratio: 4/3) {
    .name-column {
        min-width: 200px;
    }
}

.table-wrapper {
    overflow-x: auto;
}

.down-banner {
    padding: 12px 20px;
}

.events-toolbar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;

    .form-select {
        width: auto;
        max-width: 100%;
    }

    .monitor-select {
        max-width: 240px;
    }
}
</style>
