<template>
    <div class="shadow-box mb-3 p-0" :style="boxStyle">
        <div class="list-header">
            <!-- Line 1: Checkbox + Status + Tags + Search Bar -->
            <div class="filter-row">
                <div class="search-wrapper">
                    <a v-if="searchText != ''" class="search-icon" @click="clearSearchText">
                        <font-awesome-icon icon="times" />
                    </a>
                    <form @submit.prevent>
                        <input
                            ref="searchInput"
                            v-model="searchText"
                            class="form-control search-input"
                            data-testid="monitor-search"
                            :placeholder="$t('Search...')"
                            :aria-label="$t('Search monitored sites')"
                            autocomplete="off"
                            @keydown.esc="onSearchEscape"
                        />
                    </form>
                </div>

                <div class="filters-group">
                    <!-- None ticked: tick every monitor shown. Some or all: untick them -->
                    <input
                        v-if="$root.canEdit"
                        class="form-check-input"
                        data-testid="select-all-toggle"
                        type="checkbox"
                        :checked="selectMode"
                        :indeterminate="selectMode && !allMatchingSelected"
                        :aria-label="selectMode ? $t('deselectAllMonitorsAria') : $t('selectAllMonitorsAria')"
                        @change="toggleSelectAll"
                    />

                    <MonitorListFilter
                        :filterState="filterState"
                        :allCollapsed="allGroupsCollapsed"
                        :hasGroups="groupMonitors.length >= 2"
                        @update-filter="updateFilter"
                        @toggle-collapse-all="toggleCollapseAll"
                    />
                </div>
            </div>

            <!-- Line 2: Cancel + Actions (shown when selection mode is active) -->
            <div v-if="selectMode" class="selection-row">
                <button class="btn btn-outline-normal" @click="cancelSelectMode">
                    {{ $t("Cancel") }}
                </button>
                <div class="actions-wrapper">
                    <div class="dropdown">
                        <button
                            class="btn btn-outline-normal dropdown-toggle"
                            type="button"
                            data-testid="bulk-actions"
                            data-bs-toggle="dropdown"
                            :aria-label="$t('Actions')"
                            :disabled="bulkActionInProgress"
                            aria-expanded="false"
                        >
                            {{ $t("Actions") }}
                        </button>
                        <ul class="dropdown-menu">
                            <li>
                                <a
                                    class="dropdown-item"
                                    href="#"
                                    data-testid="bulk-edit"
                                    @click.prevent="$refs.bulkEditDialog.show(Object.keys(selectedMonitors))"
                                >
                                    <font-awesome-icon icon="edit" class="me-2" />
                                    {{ $t("bulkEditAction") }}
                                </a>
                            </li>
                            <li>
                                <a
                                    class="dropdown-item"
                                    href="#"
                                    data-testid="bulk-export"
                                    @click.prevent="$refs.exportDialog.show(Object.keys(selectedMonitors))"
                                >
                                    <font-awesome-icon icon="download" class="me-2" />
                                    {{ $t("exportMonitorsAction") }}
                                </a>
                            </li>
                            <li>
                                <a class="dropdown-item" href="#" data-testid="bulk-pause" @click.prevent="pauseDialog">
                                    <font-awesome-icon icon="pause" class="me-2" />
                                    {{ $t("Pause") }}
                                </a>
                            </li>
                            <li>
                                <a
                                    class="dropdown-item"
                                    href="#"
                                    data-testid="bulk-resume"
                                    @click.prevent="resumeSelected"
                                >
                                    <font-awesome-icon icon="play" class="me-2" />
                                    {{ $t("Resume") }}
                                </a>
                            </li>
                            <li>
                                <a
                                    class="dropdown-item text-danger"
                                    data-testid="bulk-delete"
                                    href="#"
                                    @click.prevent="$refs.confirmDelete.show()"
                                >
                                    <font-awesome-icon icon="trash" class="me-2" />
                                    {{ $t("Delete") }}
                                </a>
                            </li>
                        </ul>
                    </div>
                </div>
                <span class="selected-count">
                    {{ $t("selectedMonitorCountMsg", selectedMonitorCount) }}
                </span>
            </div>
        </div>
        <div
            ref="monitorList"
            class="monitor-list px-2"
            :class="{ scrollbar: scrollbar }"
            :style="monitorListStyle"
            data-testid="monitor-list"
            @scroll.passive="onListScroll"
        >
            <div v-if="!$root.monitorListLoaded" class="list-loading" aria-busy="true">
                <div v-for="i in 8" :key="i" class="skeleton-row"></div>
            </div>
            <div v-else-if="Object.keys($root.monitorList).length === 0" class="text-center mt-3">
                <template v-if="$root.canEdit">
                    {{ $t("No Monitors, please") }}
                    <router-link to="/add">{{ $t("add one") }}</router-link>
                </template>
                <template v-else>{{ $t("viewerNoMonitorsShort") }}</template>
            </div>

            <div :style="{ height: renderedRows.top + 'px' }"></div>
            <MonitorListItem
                v-for="item in renderedRows.items"
                :key="`${item.id}-${collapseKey}`"
                :ref="(el) => setRowRef(el, item.id)"
                :monitor="item"
                :children-by-parent="childrenByParent"
                :selectable="$root.canEdit"
                :isSelected="isSelected"
                :select="select"
                :deselect="deselect"
                :filter-func="filterFunc"
                :sort-func="sortFunc"
            />
            <div :style="{ height: renderedRows.bottom + 'px' }"></div>
        </div>
    </div>

    <BulkEditDialog ref="bulkEditDialog" @saved="cancelSelectMode" />
    <ExportMonitorsDialog ref="exportDialog" />

    <Confirm ref="confirmPause" :yes-text="$t('Yes')" :no-text="$t('No')" @yes="pauseSelected">
        {{ $t("pauseMonitorMsg") }}
    </Confirm>

    <Confirm ref="confirmDelete" btn-style="btn-danger" :yes-text="$t('Yes')" :no-text="$t('No')" @yes="deleteSelected">
        {{ $t("deleteMonitorsMsg") }}
        <!-- By name, because ticked monitors stay ticked when the filters hide them -->
        <ul class="delete-list mt-2 mb-0" data-testid="bulk-delete-list">
            <li v-for="m in selectedForDelete" :key="m.id">
                {{ m.path }}
                <span v-if="!filterFunc(m)" class="text-muted">({{ $t("hiddenByFilters") }})</span>
            </li>
        </ul>
    </Confirm>
</template>

<script>
import BulkEditDialog from "../components/BulkEditDialog.vue";
import Confirm from "../components/Confirm.vue";
import ExportMonitorsDialog from "../components/ExportMonitorsDialog.vue";
import MonitorListItem from "../components/MonitorListItem.vue";
import MonitorListFilter from "./MonitorListFilter.vue";
import { getMonitorRelativeURL } from "../util.ts";
import { computeListWindow, computeRowMetrics } from "../monitor-list-window";

export default {
    components: {
        BulkEditDialog,
        ExportMonitorsDialog,
        Confirm,
        MonitorListItem,
        MonitorListFilter,
    },
    props: {
        /** Should the scrollbar be shown */
        scrollbar: {
            type: Boolean,
        },
    },
    data() {
        return {
            searchText: "",
            selectedMonitors: {},
            windowTop: 0,
            bulkActionInProgress: false,
            filterState: {
                status: null,
                active: null,
                tags: null,
            },
            collapseKey: 0,
            scrollTop: 0,
            viewportHeight: 0,
            rowHeights: {},
        };
    },
    computed: {
        /**
         * Groups all monitors by parent id in a single pass so rows never scan the full list.
         * @returns {Map<number|null, Array>} Parent id -> direct children
         */
        childrenByParent() {
            const map = new Map();
            for (const monitor of Object.values(this.$root.monitorList)) {
                const key = monitor.parent ?? null;
                let children = map.get(key);
                if (!children) {
                    children = [];
                    map.set(key, children);
                }
                children.push(monitor);
            }
            return map;
        },

        /**
         * Row offsets and heights for the windowed list.
         * @returns {{heights: number[], offsets: number[], total: number}} Layout data
         */
        rowMetrics() {
            const ids = this.sortedMonitorList.map((monitor) => monitor.id);
            return computeRowMetrics(ids, this.rowHeights);
        },

        /**
         * Only the rows inside (and slightly around) the viewport, plus spacer sizes.
         * This keeps the DOM at ~50 rows no matter how many monitors exist.
         * @returns {{items: Array, top: number, bottom: number}} Visible slice and spacers
         */
        renderedRows() {
            const list = this.sortedMonitorList;
            const { heights, offsets, total } = this.rowMetrics;
            const window = computeListWindow(offsets, heights, total, this.scrollTop, this.viewportHeight);

            return {
                items: list.slice(window.start, window.end),
                top: window.top,
                bottom: window.bottom,
            };
        },

        /**
         * Improve the sticky appearance of the list by increasing its
         * height as user scrolls down.
         * Not used on mobile.
         * @returns {object} Style for monitor list
         */
        boxStyle() {
            if (window.innerWidth > 550) {
                return {
                    height: `calc(100vh - 160px + ${this.windowTop}px)`,
                };
            } else {
                return {
                    height: "calc(100vh - 160px)",
                };
            }
        },

        /**
         * Returns a sorted list of monitors based on the applied filters and search text.
         * @returns {Array} The sorted list of monitors.
         */
        sortedMonitorList() {
            let result = this.childrenByParent.get(null) ?? [];

            result = result.filter(this.filterFunc);

            result.sort(this.sortFunc);

            return result;
        },

        isDarkTheme() {
            return document.body.classList.contains("dark");
        },

        monitorListStyle() {
            // The header height has to be changed in case it is modified in the future.
            // +10px is the margin-bottom of the header
            let listHeaderHeight = 58 + 10;

            // Only add extra height when selection row is visible
            if (this.selectMode) {
                listHeaderHeight += 42;
            }

            return {
                height: `calc(100% - ${listHeaderHeight}px)`,
            };
        },

        selectedMonitorCount() {
            return Object.keys(this.selectedMonitors).length;
        },

        /**
         * The ticked monitors with their paths, for the delete confirmation. The paths come from the
         * groups as they are now, so a group renamed or moved meanwhile shows its new name.
         * @returns {{id: number, path: string}[]} Monitors, by path
         */
        selectedForDelete() {
            const list = this.$root.monitorList;
            return Object.keys(this.selectedMonitors)
                .map((id) => list[id])
                .filter(Boolean)
                .map((m) => {
                    const names = [m.name];
                    const seen = new Set([m.id]);
                    for (let p = list[m.parent]; p && !seen.has(p.id); p = list[p.parent]) {
                        seen.add(p.id);
                        names.unshift(p.name);
                    }
                    return { ...m, path: names.join(" / ") };
                })
                .sort((a, b) => a.path.localeCompare(b.path));
        },

        // Monitors are being chosen once one is ticked
        selectMode() {
            return this.selectedMonitorCount > 0;
        },

        /**
         * Every monitor that matches the filters and search, in groups too. A group shown only
         * because something in it matches isn't one: ticking it would pause or delete the rest.
         * @returns {object[]} Monitors
         */
        matchingMonitors() {
            return Object.values(this.$root.monitorList).filter(this.matchesFilters);
        },

        // The monitors' ids: deleting one from the list changes it, though the list stays the same object
        monitorIds() {
            return Object.keys(this.$root.monitorList);
        },

        allMatchingSelected() {
            return (
                this.matchingMonitors.length > 0 && this.matchingMonitors.every((m) => m.id in this.selectedMonitors)
            );
        },

        /**
         * Determines if any filters are active.
         * @returns {boolean} True if any filter is active, false otherwise.
         */
        filtersActive() {
            return (
                this.filterState.status != null ||
                this.filterState.active != null ||
                this.filterState.tags != null ||
                this.searchText !== ""
            );
        },

        /**
         * Gets all group monitors that have children at any nesting level
         * @returns {Array} Array of group monitors with children
         */
        groupMonitors() {
            return (this.childrenByParent.get(null) ?? []).filter(
                (m) => m.type === "group" && (this.childrenByParent.get(m.id)?.length ?? 0) > 0
            );
        },

        /**
         * Determines if all groups are collapsed.
         * Note: collapseKey is included to force re-computation when toggleCollapseAll()
         * updates localStorage, since Vue cannot detect localStorage changes.
         * @returns {boolean} True if all groups are collapsed
         */
        allGroupsCollapsed() {
            // collapseKey forces this computed to re-evaluate after localStorage updates
            if (this.collapseKey < 0 || this.groupMonitors.length === 0) {
                return true;
            }

            const storage = window.localStorage.getItem("monitorCollapsed");
            if (storage === null) {
                return true; // Default is collapsed
            }

            const storageObject = JSON.parse(storage);
            return this.groupMonitors.every((group) => storageObject[`monitor_${group.id}`] !== false);
        },
    },
    watch: {
        // A monitor deleted meanwhile, here or elsewhere, is no longer chosen
        monitorIds(ids) {
            const present = new Set(ids);
            for (const id of Object.keys(this.selectedMonitors)) {
                if (!present.has(id)) {
                    delete this.selectedMonitors[id];
                }
            }
        },
        filterState: {
            handler(state) {
                this.$root.monitorListFilter = state;
            },
            deep: true,
            immediate: true,
        },
        "$root.monitorListFilterRequest"() {
            this.applyFilterRequest();
        },
    },
    created() {
        this.rowRefs = new Map();
    },
    mounted() {
        window.addEventListener("scroll", this.onScroll);
        window.addEventListener("resize", this.onListScroll);
        this.onListScroll();
        this.applyFilterRequest();
        this.$root.emitter.on("focusMonitorSearch", this.focusSearch);
        this.$root.emitter.on("monitorListMove", this.moveSelection);
    },
    updated() {
        this.measureRows();
    },
    beforeUnmount() {
        this.$root.emitter.off("focusMonitorSearch", this.focusSearch);
        this.$root.emitter.off("monitorListMove", this.moveSelection);
        this.$root.monitorListFilter = null;
        window.removeEventListener("scroll", this.onScroll);
        window.removeEventListener("resize", this.onListScroll);
    },
    methods: {
        /**
         * Keep the reference to each mounted row so its real height can be measured
         * @param {object} el Row component instance
         * @param {number} id Monitor id
         * @returns {void}
         */
        setRowRef(el, id) {
            if (el) {
                this.rowRefs.set(id, el);
            } else {
                this.rowRefs.delete(id);
            }
        },
        /**
         * Read the real height of mounted rows (tags and the bottom bar change it)
         * @returns {void}
         */
        measureRows() {
            let changed = false;
            for (const [id, row] of this.rowRefs) {
                const el = row?.$el;
                if (!el) {
                    continue;
                }
                const height = el.offsetHeight;
                if (height > 0 && Math.abs((this.rowHeights[id] ?? 0) - height) > 1) {
                    this.rowHeights[id] = height;
                    changed = true;
                }
            }
            if (changed) {
                this.$nextTick(this.onListScroll);
            }
        },
        /**
         * Track the list container's scroll position for windowing
         * @returns {void}
         */
        onListScroll() {
            const el = this.$refs.monitorList;
            if (!el) {
                return;
            }
            this.scrollTop = el.scrollTop;
            this.viewportHeight = el.clientHeight;
        },
        /**
         * Handle user scroll
         * @returns {void}
         */
        onScroll() {
            if (window.top.scrollY <= 133) {
                this.windowTop = window.top.scrollY;
            } else {
                this.windowTop = 133;
            }
        },
        /**
         * Get URL of monitor
         * @param {number} id ID of monitor
         * @returns {string} Relative URL of monitor
         */
        monitorURL(id) {
            return getMonitorRelativeURL(id);
        },
        /**
         * Clear the search bar
         * @returns {void}
         */
        clearSearchText() {
            this.searchText = "";
        },
        /**
         * Put the cursor in the search box
         * @returns {void}
         */
        focusSearch() {
            this.$refs.searchInput?.focus();
            this.$refs.searchInput?.select();
        },
        /**
         * Esc in the search box clears it, or, when it's empty, leaves it
         * @returns {void}
         */
        onSearchEscape() {
            if (this.searchText !== "") {
                this.searchText = "";
            } else {
                this.$refs.searchInput?.blur();
            }
        },
        /**
         * Open the monitor after or before the one shown, in the list's order, groups opened
         * @param {number} delta 1 for the next, -1 for the one before
         * @returns {void}
         */
        moveSelection(delta) {
            const links = [...(this.$refs.monitorList?.querySelectorAll("a.item") ?? [])];
            if (links.length === 0) {
                return;
            }
            const current = links.findIndex((a) => a.getAttribute("href") === this.$route.path);
            let next;
            if (current < 0) {
                next = delta > 0 ? 0 : links.length - 1;
            } else {
                next = Math.min(Math.max(current + delta, 0), links.length - 1);
            }
            if (next === current) {
                return;
            }
            links[next].scrollIntoView({ block: "nearest" });
            this.$router.push(links[next].getAttribute("href"));
        },
        /**
         * Update the MonitorList Filter
         * @param {object} newFilter Object with new filter
         * @returns {void}
         */
        updateFilter(newFilter) {
            this.filterState = newFilter;
        },
        /**
         * Apply the status filter asked for elsewhere, such as by the dashboard's counts,
         * keeping the tags chosen
         * @returns {void}
         */
        applyFilterRequest() {
            const request = this.$root.monitorListFilterRequest;
            if (!request) {
                return;
            }
            this.$root.monitorListFilterRequest = null;
            this.filterState = {
                ...this.filterState,
                status: request.status,
                active: request.active,
            };
            if (this.$refs.monitorList) {
                this.$refs.monitorList.scrollTop = 0;
            }
        },
        /**
         * Toggle collapse state for all group monitors
         * If collapsing all groups while viewing a nested group, navigate to its root parent
         * @returns {void}
         */
        toggleCollapseAll() {
            const shouldCollapse = !this.allGroupsCollapsed;

            let storageObject = {};
            const storage = window.localStorage.getItem("monitorCollapsed");
            if (storage !== null) {
                storageObject = JSON.parse(storage);
            }

            this.groupMonitors.forEach((group) => {
                storageObject[`monitor_${group.id}`] = shouldCollapse;
            });

            window.localStorage.setItem("monitorCollapsed", JSON.stringify(storageObject));

            // If collapsing all and currently viewing a nested group, navigate to root parent
            if (shouldCollapse) {
                const currentMonitorId = parseInt(this.$route.params.id);
                const currentMonitor = this.$root.monitorList[currentMonitorId];

                if (currentMonitor && currentMonitor.parent !== null) {
                    // Find the root parent by traversing up the hierarchy
                    let rootParentId = currentMonitor.parent;
                    let rootParent = this.$root.monitorList[rootParentId];

                    while (rootParent && rootParent.parent !== null) {
                        rootParentId = rootParent.parent;
                        rootParent = this.$root.monitorList[rootParentId];
                    }

                    // Navigate to the root parent, then increment collapseKey to force re-render
                    this.$router.push(getMonitorRelativeURL(rootParentId)).finally(() => {
                        this.collapseKey++;
                    });
                    return;
                }
            }

            this.collapseKey++;
        },
        /**
         * Deselect a monitor
         * @param {number} id ID of monitor
         * @returns {void}
         */
        deselect(id) {
            delete this.selectedMonitors[id];
        },
        /**
         * Select a monitor
         * @param {number} id ID of monitor
         * @returns {void}
         */
        select(id) {
            this.selectedMonitors[id] = true;
        },
        /**
         * Determine if monitor is selected
         * @param {number} id ID of monitor
         * @returns {bool} Is the monitor selected?
         */
        isSelected(id) {
            return id in this.selectedMonitors;
        },
        /**
         * Disable select mode and reset selection
         * @returns {void}
         */
        cancelSelectMode() {
            this.selectedMonitors = {};
        },
        /**
         * The header's box: with none ticked, tick every monitor shown; otherwise untick them all
         * @returns {void}
         */
        toggleSelectAll() {
            if (this.selectMode) {
                this.selectedMonitors = {};
                return;
            }
            const selected = {};
            for (const m of this.matchingMonitors) {
                selected[m.id] = true;
            }
            this.selectedMonitors = selected;
        },
        /**
         * Show dialog to confirm pause
         * @returns {void}
         */
        pauseDialog() {
            this.$refs.confirmPause.show();
        },
        /**
         * Pause each selected monitor
         * @returns {void}
         */
        pauseSelected() {
            if (this.bulkActionInProgress) {
                return;
            }

            const activeMonitors = Object.keys(this.selectedMonitors).filter(
                (id) => this.$root.monitorList[id]?.active
            );

            if (activeMonitors.length === 0) {
                this.$root.toastError(this.$t("noMonitorsPausedMsg"));
                return;
            }

            this.setSelectedActive(activeMonitors, false);
        },
        /**
         * Resume each selected monitor
         * @returns {void}
         */
        resumeSelected() {
            if (this.bulkActionInProgress) {
                return;
            }

            const inactiveMonitors = Object.keys(this.selectedMonitors).filter(
                (id) => this.$root.monitorList[id] && !this.$root.monitorList[id].active
            );

            if (inactiveMonitors.length === 0) {
                this.$root.toastError(this.$t("noMonitorsResumedMsg"));
                return;
            }

            this.setSelectedActive(inactiveMonitors, true);
        },
        /**
         * Pause or resume monitors in one request, so the list changes once rather than per monitor
         * @param {string[]} ids Monitors to change
         * @param {boolean} active Resume them, or pause them
         * @returns {void}
         */
        setSelectedActive(ids, active) {
            this.bulkActionInProgress = true;
            this.$root.getSocket().emit("setMonitorsActive", ids.map(Number), active, (res) => {
                this.bulkActionInProgress = false;
                if (!res?.ok) {
                    this.$root.toastError(res?.msg ?? this.$t("bulkActionFailedMsg"));
                    return;
                }
                this.$root.toastSuccess(this.$t(active ? "resumedMonitorsMsg" : "pausedMonitorsMsg", res.count));
                this.cancelSelectMode();
            });
        },
        /**
         * Delete each selected monitor
         * @returns {Promise<void>}
         */
        async deleteSelected() {
            if (this.bulkActionInProgress) {
                return;
            }

            const monitorIds = Object.keys(this.selectedMonitors);

            this.bulkActionInProgress = true;
            let successCount = 0;
            let errorCount = 0;

            for (const id of monitorIds) {
                try {
                    await new Promise((resolve, reject) => {
                        this.$root.getSocket().emit("deleteMonitor", id, false, (res) => {
                            if (res.ok) {
                                successCount++;
                                resolve();
                            } else {
                                errorCount++;
                                reject();
                            }
                        });
                    });
                } catch (error) {
                    // Error already counted
                }
            }

            this.bulkActionInProgress = false;

            if (successCount > 0) {
                this.$root.toastSuccess(this.$t("deletedMonitorsMsg", successCount));
            }
            if (errorCount > 0) {
                this.$root.toastError(this.$t("bulkDeleteErrorMsg", errorCount));
            }

            this.cancelSelectMode();
        },
        /**
         * Whether a monitor should be displayed based on the filters
         * @param {object} monitor Monitor to check
         * @returns {boolean} Should the monitor be displayed
         */
        filterFunc(monitor) {
            // Group monitors bypass filter if at least 1 of children matched
            if (monitor.type === "group") {
                const children = this.childrenByParent.get(monitor.id) ?? [];
                if (children.some((child) => this.filterFunc(child))) {
                    return true;
                }
            }
            return this.matchesFilters(monitor);
        },
        /**
         * Whether a monitor itself matches the filters and search, whatever is in it
         * @param {object} monitor Monitor to check
         * @returns {boolean} Does it match
         */
        matchesFilters(monitor) {
            // filter by search text
            // finds monitor name, tag name or tag value
            let searchTextMatch = true;
            if (this.searchText !== "") {
                const loweredSearchText = this.searchText.toLowerCase();
                searchTextMatch =
                    monitor.name.toLowerCase().includes(loweredSearchText) ||
                    monitor.tags.find(
                        (tag) =>
                            tag.name.toLowerCase().includes(loweredSearchText) ||
                            tag.value?.toLowerCase().includes(loweredSearchText)
                    );
            }

            // filter by status
            let statusMatch = true;
            if (this.filterState.status != null && this.filterState.status.length > 0) {
                if (!monitor.active) {
                    monitor.status = null;
                } else {
                    monitor.status = this.$root.lastHeartbeatList[monitor.id]?.status;
                }
                statusMatch = this.filterState.status.includes(monitor.status);
            }

            // filter by active
            let activeMatch = true;
            if (this.filterState.active != null && this.filterState.active.length > 0) {
                activeMatch = this.filterState.active.includes(monitor.active);
            }

            // filter by tags
            let tagsMatch = true;
            if (this.filterState.tags != null && this.filterState.tags.length > 0) {
                tagsMatch =
                    monitor.tags
                        .map((tag) => tag.tag_id) // convert to array of tag IDs
                        .filter((monitorTagId) => this.filterState.tags.includes(monitorTagId)).length > 0; // perform Array Intersaction between filter and monitor's tags
            }

            return searchTextMatch && statusMatch && activeMatch && tagsMatch;
        },
        /**
         * Function used in Array.sort to order monitors in a list.
         * @param {*} m1 monitor 1
         * @param {*} m2 monitor 2
         * @returns {number} -1, 0 or 1
         */
        sortFunc(m1, m2) {
            if (!m1 || !m2) {
                return 0;
            }
            // A monitor may be added before its name arrives (e.g. optimistic update)
            const name1 = typeof m1.name === "string" ? m1.name : "";
            const name2 = typeof m2.name === "string" ? m2.name : "";
            if (m1.active !== m2.active) {
                if (m1.active === false) {
                    return 1;
                }

                if (m2.active === false) {
                    return -1;
                }
            }

            if (m1.weight !== m2.weight) {
                if (m1.weight > m2.weight) {
                    return -1;
                }

                if (m1.weight < m2.weight) {
                    return 1;
                }
            }

            return name1.localeCompare(name2);
        },
    },
};
</script>

<style lang="scss" scoped>
@import "../assets/vars.scss";

.shadow-box {
    height: calc(100vh - 150px);
    position: sticky;
    top: 10px;
}

.small-padding {
    padding-left: 5px !important;
    padding-right: 5px !important;
}

.list-header {
    border-bottom: 1px solid #dee2e6;
    border-radius: 10px 10px 0 0;
    margin-bottom: 10px;
    padding: 10px;
    display: flex;
    flex-direction: column;
    gap: 8px;

    .dark & {
        background-color: $dark-header-bg;
        border-bottom: 0;
    }
}

.filter-row {
    display: flex;
    justify-content: flex-start;
    align-items: center;
    gap: 8px;
    flex-wrap: nowrap;
    width: 100%;

    .form-check-input {
        cursor: pointer;
        margin: 0;
        margin-left: 6px;
        flex-shrink: 0;
    }
}

.filters-group {
    display: flex;
    align-items: center;
    gap: 8px;
}

.actions-wrapper {
    display: flex;
    align-items: center;

    .dropdown-toggle {
        white-space: nowrap;

        &:disabled {
            opacity: 0.5;
            cursor: not-allowed;
        }
    }

    .dropdown-menu {
        min-width: 140px;
        padding: 4px 0;
        border-radius: 8px;
        box-shadow: 0 2px 8px rgba(0, 0, 0, 0.15);

        .dark & {
            background-color: $dark-bg;
            border-color: $dark-border-color;
            box-shadow: 0 2px 8px rgba(0, 0, 0, 0.3);
        }
    }

    .dropdown-item {
        cursor: pointer;
        padding: 6px 12px;
        font-size: 0.9em;

        .dark & {
            color: $dark-font-color;

            &:hover {
                background-color: $dark-bg2;
                color: $dark-font-color;
            }
        }

        &.text-danger {
            color: #dc3545;

            .dark & {
                color: #dc3545;
            }

            &:hover {
                background-color: #dc3545 !important;
                color: white !important;

                .dark & {
                    background-color: #dc3545 !important;
                    color: white !important;
                }

                svg {
                    color: white !important;
                }
            }
        }
    }
}

.selection-row {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
}

.selected-count {
    white-space: nowrap;
    font-size: 0.9em;
    color: $primary;

    .dark & {
        color: $dark-font-color;
    }
}

.selection-controls {
    margin-top: 5px;
    display: flex;
    align-items: center;

    .d-flex {
        width: 100%;
    }

    .gap-2 {
        gap: 0.5rem;
    }

    .selected-count {
        margin-left: auto;
    }
}

@media (max-width: 975px) {
    .filter-row {
        flex-direction: column-reverse;
        align-items: stretch;
        gap: 8px;
    }

    .search-wrapper {
        width: 100% !important;
        max-width: 100% !important;
        margin-left: 0 !important;
        flex: 1 1 100%;
    }

    .filters-group {
        width: 100%;
    }
}

@media (max-width: 770px) {
    .list-header {
        margin-bottom: 10px;
        padding: 20px;
    }
}

.search-wrapper {
    display: flex;
    align-items: center;
    position: relative;
    flex: 1 1 auto;
    min-width: 0;
    max-width: 300px;
    margin-left: auto;
    order: 1;

    form {
        width: 100%;
    }
}

.search-icon {
    position: absolute;
    right: 10px;
    color: #c0c0c0;
    cursor: pointer;
    transition: all ease-in-out 0.1s;
    z-index: 1;

    &:hover {
        opacity: 0.5;
    }
}

.search-input {
    width: 100%;
    padding-right: 30px;
    transition: none !important;
}

.list-loading {
    padding: 10px 4px;
}

.skeleton-row {
    height: 48px;
    margin-bottom: 8px;
    border-radius: 8px;
    background: linear-gradient(90deg, rgba(128, 128, 128, 0.08), rgba(128, 128, 128, 0.18), rgba(128, 128, 128, 0.08));
    background-size: 200% 100%;
    animation: skeleton-shimmer 1.2s ease-in-out infinite;
}

@keyframes skeleton-shimmer {
    0% {
        background-position: 200% 0;
    }

    100% {
        background-position: -200% 0;
    }
}

.tags {
    margin-top: 4px;
    padding-left: 67px;
    display: flex;
    flex-wrap: wrap;
    gap: 0;
}

@media (max-width: 549px), (min-width: 770px) and (max-width: 1149px), (min-width: 1200px) and (max-width: 1499px) {
    .selection-controls {
        .selected-count {
            margin-left: 0;
            width: 100%;
            margin-top: 0.25rem;
        }
    }
}

.delete-list {
    max-height: 240px;
    overflow-y: auto;
    padding-left: 1.25rem;
}
</style>
