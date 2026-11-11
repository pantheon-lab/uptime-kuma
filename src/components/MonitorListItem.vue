<template>
    <div>
        <div
            class="draggable-item"
            :data-monitor-id="monitor.id"
            :style="depthMargin"
            :class="{
                'drag-over': dragOverCount > 0 && dropAt === 'into',
                'drop-before': dragOverCount > 0 && dropAt === 'before',
                'drop-after': dragOverCount > 0 && dropAt === 'after',
                selectable,
            }"
            @dragstart="onDragStart"
            @dragend="onDragEnd"
            @dragenter.prevent="onDragEnter"
            @dragleave.prevent="onDragLeave"
            @dragover.prevent="onDragOver"
            @drop.prevent="onDrop"
        >
            <!-- Checkbox, in a label as tall as the row, so that the space around the box ticks it too -->
            <label v-if="selectable" class="select-input-wrapper" @click.stop>
                <input
                    class="form-check-input select-input"
                    type="checkbox"
                    :data-testid="'monitor-select-' + monitor.id"
                    :aria-label="$t('selectMonitorAria', [monitor.name])"
                    :checked="isSelected(monitor.id)"
                    @click.stop="toggleSelection"
                />
            </label>

            <!-- While monitors are being chosen, a click anywhere on the row ticks or unticks it -->
            <router-link v-slot="{ href, navigate, isActive }" :to="monitorURL(monitor.id)" custom>
                <a
                    :href="href"
                    class="item"
                    :class="{
                        disabled: !monitor.active,
                        active: isActive,
                        selected: selectMode && isSelected(monitor.id),
                    }"
                    :data-testid="'monitor-row-' + monitor.id"
                    @click="onRowClick($event, navigate)"
                >
                    <div class="row">
                        <div class="small-padding d-flex gap-2 align-items-center" :class="monitorStyle">
                            <div class="me-1">
                                <Uptime :monitor="monitor" type="24" :pill="true" />
                            </div>
                            <div class="d-flex align-items-center gap-2 flex-fill" style="min-width: 0">
                                <!-- A button, so that the keyboard opens and closes groups too -->
                                <button
                                    v-if="hasChildren"
                                    type="button"
                                    class="collapse-padding collapse-button"
                                    data-testid="monitor-collapse"
                                    :aria-expanded="!isCollapsed"
                                    :aria-label="$t(isCollapsed ? 'expandGroup' : 'collapseGroup', [monitor.name])"
                                    @click.prevent.stop="changeCollapsed"
                                >
                                    <font-awesome-icon
                                        icon="chevron-down"
                                        class="animated"
                                        :class="{ collapsed: isCollapsed }"
                                    />
                                </button>
                                <div class="flex-fill text-truncate" style="min-width: 0">
                                    <div class="text-truncate">{{ monitor.name }}</div>
                                    <div v-if="monitor.tags.length > 0" class="tags gap-1">
                                        <Tag
                                            v-for="tag in monitor.tags"
                                            :key="tag"
                                            :item="tag"
                                            :size="'sm'"
                                            :title="tag.name"
                                        />
                                    </div>
                                </div>
                            </div>
                        </div>
                        <div
                            v-if="$root.userHeartbeatBar == 'normal'"
                            :key="$root.userHeartbeatBar"
                            class="col-3 col-xl-6"
                        >
                            <HeartbeatBar ref="heartbeatBar" size="small" :monitor-id="monitor.id" />
                        </div>
                    </div>

                    <div v-if="$root.userHeartbeatBar == 'bottom'" class="row">
                        <div class="col-12 bottom-style">
                            <HeartbeatBar ref="heartbeatBar" size="small" :monitor-id="monitor.id" />
                        </div>
                    </div>
                </a>
            </router-link>
        </div>

        <transition name="slide-fade-up">
            <div v-if="!isCollapsed" class="childs">
                <MonitorListItem
                    v-for="(item, index) in sortedChildMonitorList"
                    :key="index"
                    :monitor="item"
                    :children-by-parent="childrenByParent"
                    :selectable="selectable"
                    :select-mode="selectMode"
                    :isSelected="isSelected"
                    :select="select"
                    :deselect="deselect"
                    :depth="depth + 1"
                    :filter-func="filterFunc"
                    :sort-func="sortFunc"
                    :custom-order="customOrder"
                    :reorder="reorder"
                />
            </div>
        </transition>
    </div>
</template>

<script>
import HeartbeatBar from "../components/HeartbeatBar.vue";
import Tag from "../components/Tag.vue";
import Uptime from "../components/Uptime.vue";
import { getMonitorRelativeURL } from "../util.ts";
import { dropPosition } from "../monitor-sort";

// The monitor being dragged, for the rows it passes over
let currentDragId = null;
import { rowClickAction } from "../row-click";

export default {
    name: "MonitorListItem",
    components: {
        Uptime,
        HeartbeatBar,
        Tag,
    },
    props: {
        /** Parent id -> children index, shared by the whole list */
        childrenByParent: {
            type: Map,
            default: () => new Map(),
        },
        /** Monitor this represents */
        monitor: {
            type: Object,
            default: null,
        },
        /** Whether the monitor has a box to choose it for the list's actions */
        selectable: {
            type: Boolean,
            default: false,
        },
        /** How many ancestors are above this monitor */
        depth: {
            type: Number,
            default: 0,
        },
        /** Whether any monitor is ticked: a click on a row then ticks it rather than opening it */
        selectMode: {
            type: Boolean,
            default: false,
        },
        /** Callback to determine if monitor is selected */
        isSelected: {
            type: Function,
            default: () => {},
        },
        /** Callback fired when monitor is selected */
        select: {
            type: Function,
            default: () => {},
        },
        /** Callback fired when monitor is deselected */
        deselect: {
            type: Function,
            default: () => {},
        },
        /** Function to filter child monitors */
        filterFunc: {
            type: Function,
            default: () => {},
        },
        /** Function to sort child monitors */
        sortFunc: {
            type: Function,
            default: () => {},
        },
        /** Whether the list is in the custom order, which dragging a row above or below another sets */
        customOrder: {
            type: Boolean,
            default: false,
        },
        /** Moves a monitor before or after another: reorder(draggedId, targetId, "before" or "after") */
        reorder: {
            type: Function,
            default: () => {},
        },
    },
    data() {
        return {
            isCollapsed: true,
            dragOverCount: 0,
            // Where a drag over this row would drop: "before", "after" or "into" a group
            dropAt: "into",
        };
    },
    computed: {
        sortedChildMonitorList() {
            let result = this.childrenByParent.get(this.monitor.id) ?? [];

            // Run filter on children
            result = result.filter(this.filterFunc);

            result.sort(this.sortFunc);

            return result;
        },
        hasChildren() {
            return this.sortedChildMonitorList.length > 0;
        },
        depthMargin() {
            return {
                marginLeft: `${20 * this.depth}px`,
            };
        },
        monitorStyle() {
            const isFullWidth = this.$root.userHeartbeatBar === "bottom" || this.$root.userHeartbeatBar === "none";
            const c = {};
            if (!isFullWidth) {
                c["col-9"] = true;
                c["col-xl-6"] = true;
            }
            return c;
        },
    },
    beforeMount() {
        // Always unfold if monitor is accessed directly
        if (this.monitor.childrenIDs.includes(parseInt(this.$route.params.id))) {
            this.isCollapsed = false;
            return;
        }

        // Set collapsed value based on local storage
        let storage = window.localStorage.getItem("monitorCollapsed");
        if (storage === null) {
            return;
        }

        let storageObject = JSON.parse(storage);
        if (storageObject[`monitor_${this.monitor.id}`] == null) {
            return;
        }

        this.isCollapsed = storageObject[`monitor_${this.monitor.id}`];
    },
    methods: {
        /**
         * Changes the collapsed value of the current monitor and saves
         * it to local storage
         * @returns {void}
         */
        changeCollapsed() {
            this.isCollapsed = !this.isCollapsed;

            // Save collapsed value into local storage
            let storage = window.localStorage.getItem("monitorCollapsed");
            let storageObject = {};
            if (storage !== null) {
                storageObject = JSON.parse(storage);
            }
            storageObject[`monitor_${this.monitor.id}`] = this.isCollapsed;

            window.localStorage.setItem("monitorCollapsed", JSON.stringify(storageObject));
        },
        /**
         * Initializes the drag operation if the monitor is draggable.
         * @param {DragEvent} event - The dragstart event triggered by the browser.
         * @returns {void} This method does not return anything.
         */
        onDragStart(event) {
            // A viewer's move could only fail
            if (!this.$root.canEdit) {
                event.preventDefault();
                return;
            }
            // dragover can't read the drag's data, so the rows it passes over find the monitor here
            currentDragId = this.monitor.id;
            try {
                event.dataTransfer.setData("text/monitor-id", String(this.monitor.id));
                event.dataTransfer.effectAllowed = "move";
            } catch (e) {
                // ignore
            }
        },

        onDragEnd() {
            currentDragId = null;
        },

        /**
         * Whether the monitor being dragged can be put before or after this one: in the custom
         * order, among the monitors of the same group
         * @returns {boolean} Whether it can
         */
        canReorderHere() {
            if (!this.customOrder || currentDragId == null || currentDragId === this.monitor.id) {
                return false;
            }
            const dragged = this.$root.monitorList[currentDragId];
            return !!dragged && (dragged.parent ?? null) === (this.monitor.parent ?? null);
        },

        onDragEnter() {
            if (this.monitor.type !== "group" && !this.canReorderHere()) {
                return;
            }
            this.dragOverCount++;
        },

        onDragLeave() {
            if (this.dragOverCount > 0) {
                this.dragOverCount--;
            }
        },

        /**
         * Show where the monitor would land: a line above or below this row, or this group outlined
         * @param {DragEvent} event The drag
         * @returns {void}
         */
        onDragOver(event) {
            if (this.dragOverCount === 0) {
                return;
            }
            const isGroup = this.monitor.type === "group";
            if (!this.canReorderHere()) {
                this.dropAt = "into";
                return;
            }
            const rect = event.currentTarget.getBoundingClientRect();
            this.dropAt = dropPosition(event.clientY - rect.top, rect.height, isGroup);
        },

        async onDrop(event) {
            // Where it lands comes from the drop itself: the last dragover may not have been where it ended
            let at = "into";
            if (this.canReorderHere()) {
                const rect = event.currentTarget.getBoundingClientRect();
                at = dropPosition(event.clientY - rect.top, rect.height, this.monitor.type === "group");
            }
            this.dragOverCount = 0;
            this.dropAt = "into";

            if ((at === "before" || at === "after") && this.canReorderHere()) {
                this.reorder(currentDragId, this.monitor.id, at);
                return;
            }

            // Only groups accept drops
            if (this.monitor.type !== "group") {
                return;
            }

            const draggedId = event.dataTransfer.getData("text/monitor-id");
            if (!draggedId) {
                return;
            }

            const draggedMonitorId = parseInt(draggedId);
            if (isNaN(draggedMonitorId) || draggedMonitorId === this.monitor.id) {
                return;
            }

            const draggedMonitor = this.$root.monitorList[draggedMonitorId];
            if (!draggedMonitor) {
                return;
            }

            const parentId = this.monitor.id;
            if (draggedMonitor.parent === parentId) {
                return;
            }

            // Never move a group into itself or one of its descendants
            let ancestorId = parentId;
            while (ancestorId != null) {
                if (ancestorId === draggedMonitorId) {
                    this.$root.toastError("Cannot move a group into its own subgroup");
                    return;
                }
                ancestorId = this.$root.monitorList[ancestorId]?.parent ?? null;
            }

            // Save original parent so we can revert locally if server returns error
            const originalParent = draggedMonitor.parent;
            const revert = () => {
                if (this.$root.monitorList[draggedMonitorId]) {
                    this.$root.monitorList[draggedMonitorId].parent = originalParent;
                }
            };

            // Optimistically update local state so UI updates immediately
            this.$root.monitorList[draggedMonitorId].parent = parentId;

            // The dashboard list only holds summary fields, so load the full monitor first:
            // saving the summary would clear the monitor's other settings
            this.$root.getSocket().emit("getMonitor", draggedMonitorId, (res) => {
                if (!res || !res.ok) {
                    revert();
                    return;
                }

                const monitorToSave = res.monitor;
                monitorToSave.parent = parentId;

                try {
                    this.$root.getSocket().emit("editMonitor", monitorToSave, (saveRes) => {
                        if (!saveRes || !saveRes.ok) {
                            revert();
                            if (saveRes && saveRes.msg) {
                                this.$root.toastError(saveRes.msg);
                            }
                        } else {
                            this.$root.toastRes({
                                ok: true,
                                msg: this.$t("movedIntoGroup", [draggedMonitor.name, this.monitor.name]),
                            });
                            this.showMoved(draggedMonitorId);
                        }
                    });
                } catch (e) {
                    revert();
                }
            });
        },
        /**
         * After a drop: open this group, and give the moved monitor focus, which the drop lost
         * @param {number} id The moved monitor
         * @returns {void}
         */
        showMoved(id) {
            if (this.isCollapsed) {
                this.changeCollapsed();
            }
            // The list draws the moved row again under its new group
            this.$nextTick(() =>
                requestAnimationFrame(() =>
                    document.querySelector(`.draggable-item[data-monitor-id="${id}"] a.item`)?.focus()
                )
            );
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
         * Toggle selection of monitor
         * @returns {void}
         */
        /**
         * Open the monitor, or while monitors are being chosen, tick or untick it
         * @param {MouseEvent} event The click
         * @param {Function} navigate The link's own navigation
         * @returns {void}
         */
        onRowClick(event, navigate) {
            const action = rowClickAction(event, this.selectMode, this.selectable);
            if (action === "toggle") {
                event.preventDefault();
                this.toggleSelection();
            } else if (action === "navigate") {
                navigate(event);
            }
            // "browser": the link's own behaviour, such as a new tab
        },
        toggleSelection() {
            if (this.isSelected(this.monitor.id)) {
                this.deselect(this.monitor.id);
            } else {
                this.select(this.monitor.id);
            }
        },
    },
};
</script>

<style lang="scss" scoped>
@import "../assets/vars.scss";

.collapse-button {
    padding: 2px 4px;
    border: 0;
    border-radius: 6px;
    background: none;
    color: inherit;

    &:focus-visible {
        outline: 2px solid $primary;
        outline-offset: 1px;
    }
}

.small-padding {
    padding-left: 5px !important;
    padding-right: 5px !important;
}

.tags {
    margin-top: 4px;
    padding-left: 4px;
    display: flex;
    flex-wrap: wrap;
    gap: 0;
}

.collapsed {
    transform: rotate(-90deg);
}

.animated {
    transition: all 0.2s $easing-in;
}

// The box and the space around it, as tall as the row, are one target
.draggable-item.selectable {
    display: flex;
    align-items: stretch;

    .item {
        flex: 1;
        min-width: 0;
    }
}

.select-input-wrapper {
    display: flex;
    align-items: center;
    flex-shrink: 0;
    margin: 0;
    padding: 0 8px 0 10px;
    cursor: pointer;
    position: relative;
    z-index: 15;
    border-radius: 10px;

    &:hover {
        background-color: rgba($primary, 0.08);
    }
}

.select-input {
    width: 1.3rem;
    height: 1.3rem;
    margin: 0;
    cursor: pointer;
}

.item.selected {
    background-color: rgba($primary, 0.14);
}

.dark .item.selected {
    background-color: rgba($primary, 0.2);
}

.drag-over {
    border: 4px dashed $primary;
    border-radius: 0.5rem;
    background-color: $highlight-white;
}

.dark {
    .drag-over {
        background-color: $dark-bg2;
    }
}

/* -4px on all due to border-width */
.monitor-list .drag-over .item {
    padding: 9px 11px 6px 11px;
}

.draggable-item {
    cursor: grab;
    position: relative;

    /* We don't want the padding change due to the border animated */
    .item {
        padding: 12px 15px;
        transition: none !important;
    }

    &.dragging {
        cursor: grabbing;
    }
}

.bottom-style {
    margin-left: -10px;
    margin-top: 5px;
}

// Where a dragged monitor would land in the custom order
.drop-before,
.drop-after {
    position: relative;

    &::after {
        content: "";
        position: absolute;
        left: 8px;
        right: 8px;
        height: 3px;
        border-radius: 2px;
        background-color: $primary;
        pointer-events: none;
        z-index: 20;
    }
}

.drop-before::after {
    top: -2px;
}

.drop-after::after {
    bottom: -2px;
}
</style>
