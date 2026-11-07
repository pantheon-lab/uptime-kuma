<template>
    <transition ref="tableContainer" name="slide-fade" appear>
        <div v-if="$route.name === 'DashboardHome'">
            <h1 class="mb-3">
                {{ $t("Quick Stats") }}
            </h1>

            <div class="shadow-box big-padding text-center mb-3">
                <div class="row">
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

            <div class="shadow-box table-shadow-box table-wrapper">
                <div class="mb-3 text-end">
                    <button
                        class="btn btn-sm btn-outline-danger"
                        :disabled="clearingAllEvents"
                        @click="clearAllEventsDialog"
                    >
                        {{ $t("Clear All Events") }}
                    </button>
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
                            <td :colspan="tableColumnCount">
                                {{ $t("No important events") }}
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
    <router-view ref="child" />
</template>

<script>
import Status from "../components/Status.vue";
import Datetime from "../components/Datetime.vue";
import Pagination from "v-pagination-3";
import Confirm from "../components/Confirm.vue";
import { DOWN, MAINTENANCE, UP } from "../util";

export default {
    components: {
        Datetime,
        Status,
        Pagination,
        Confirm,
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
            if (this.page === 1) {
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
            this.$root.getSocket().emit("monitorImportantHeartbeatListCount", null, (res) => {
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
            this.$root.getSocket().emit("monitorImportantHeartbeatListPaged", null, offset, this.perPage, (res) => {
                if (res.ok) {
                    this.displayedRecords = res.data;
                }
            });
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

        clearAllEventsDialog() {
            this.$refs.confirmClearEvents.show();
        },
        clearAllEvents() {
            this.clearingAllEvents = true;
            const monitorIDs = Object.keys(this.$root.monitorList);
            let failed = 0;
            const total = monitorIDs.length;

            if (total === 0) {
                this.clearingAllEvents = false;
                this.$root.toastError(this.$t("No monitors found"));
                return;
            }

            monitorIDs.forEach((monitorID) => {
                this.$root.getSocket().emit("clearEvents", monitorID, (res) => {
                    if (!res || !res.ok) {
                        failed++;
                    }
                });
            });
            this.clearingAllEvents = false;
            this.page = 1;
            this.getImportantHeartbeatListLength();
            if (failed === 0) {
                this.$root.toastSuccess(this.$t("Events cleared successfully"));
            } else {
                this.$root.toastError(
                    this.$t("Could not clear events", {
                        failed,
                        total,
                    })
                );
            }
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
</style>
