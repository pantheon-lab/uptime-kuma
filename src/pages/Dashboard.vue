<template>
    <div class="container-fluid">
        <div ref="row" class="row" :class="{ 'resizable-row': !$root.isMobile }">
            <div
                v-if="!$root.isMobile"
                ref="listPanel"
                class="list-panel ps-0"
                :class="{ resizing }"
                :style="listPanelStyle"
                data-testid="list-panel"
            >
                <div>
                    <router-link v-if="$root.canEdit" to="/add" class="btn btn-primary mb-3">
                        <font-awesome-icon icon="plus" />
                        {{ $t("Add New Monitor") }}
                    </router-link>
                    <router-link
                        v-if="$root.canEdit"
                        to="/add?import=1"
                        class="btn btn-outline-normal mb-3 ms-2"
                        data-testid="import-monitors-link"
                    >
                        <font-awesome-icon icon="upload" />
                        {{ $t("importMonitorsButton") }}
                    </router-link>
                </div>
                <MonitorList :scrollbar="true" />
            </div>

            <!-- The list's edge: drag it, or use the arrow keys, Home and End. Enter or a double click resets it. -->
            <div
                v-if="!$root.isMobile"
                class="panel-resizer"
                :class="{ resizing }"
                role="separator"
                tabindex="0"
                aria-orientation="vertical"
                :aria-label="$t('resizeMonitorList')"
                :title="$t('resizeMonitorListHint')"
                :aria-valuenow="currentWidth"
                :aria-valuemin="minWidth"
                :aria-valuemax="maxWidth"
                data-testid="panel-resizer"
                @pointerdown="startResize"
                @keydown="onResizeKey"
                @dblclick="setWidth(null)"
            >
                <span class="grip" aria-hidden="true"></span>
            </div>

            <div
                ref="container"
                class="mb-3 gx-0"
                :class="$root.isMobile ? 'col-12' : 'detail-panel'"
                data-testid="detail-panel"
            >
                <!-- Add :key to disable vue router re-use the same component -->
                <router-view :key="$route.fullPath" :calculatedHeight="height" />
            </div>
        </div>
    </div>
</template>

<script>
import MonitorList from "../components/MonitorList.vue";
import { MIN_LIST_WIDTH, clampListWidth, keyListWidth, maxListWidth, parseListWidth } from "../panel-width";

// The list's width is per browser
const WIDTH_STORAGE_KEY = "monitorListWidth";

export default {
    components: {
        MonitorList,
    },
    data() {
        let stored = null;
        try {
            stored = parseListWidth(window.localStorage.getItem(WIDTH_STORAGE_KEY));
        } catch {
            // Private browsing: the layout's own width
        }
        return {
            height: 0,
            // Pixels, or null for the layout's own share of the row
            listWidth: stored,
            rowWidth: 0,
            // The list's width as drawn, for the separator's value
            currentWidth: 0,
            resizing: false,
            minWidth: MIN_LIST_WIDTH,
        };
    },
    computed: {
        maxWidth() {
            return Math.round(maxListWidth(this.rowWidth));
        },
        listPanelStyle() {
            if (this.listWidth == null || !this.rowWidth) {
                return {};
            }
            return { width: `${clampListWidth(this.listWidth, this.rowWidth)}px` };
        },
    },
    mounted() {
        this.height = this.$refs.container.offsetHeight;
        window.addEventListener("resize", this.measure);
        this.measure();
    },
    beforeUnmount() {
        window.removeEventListener("resize", this.measure);
        this.stopResize();
    },
    methods: {
        /**
         * Read the row's and the list's widths, after a change to the window or the list
         * @returns {void}
         */
        measure() {
            this.rowWidth = this.$refs.row?.clientWidth ?? 0;
            this.$nextTick(() => {
                this.currentWidth = Math.round(this.$refs.listPanel?.offsetWidth ?? 0);
            });
        },

        /**
         * Set the list's width and remember it, or go back to the layout's own with null. The list's
         * heartbeat bars and rows are sized from the window, so they're told it changed.
         * @param {number|null} width Pixels
         * @param {boolean} save Remember it in this browser
         * @returns {void}
         */
        setWidth(width, save = true) {
            this.listWidth = width == null ? null : clampListWidth(width, this.rowWidth);
            if (save) {
                try {
                    if (this.listWidth == null) {
                        window.localStorage.removeItem(WIDTH_STORAGE_KEY);
                    } else {
                        window.localStorage.setItem(WIDTH_STORAGE_KEY, String(this.listWidth));
                    }
                } catch {
                    // Private browsing: it lasts until the page is closed
                }
            }
            this.notifyResize();
        },

        /**
         * Tell the list's bars about the new width, once a frame at most
         * @returns {void}
         */
        notifyResize() {
            if (this.resizeFrame) {
                return;
            }
            this.resizeFrame = requestAnimationFrame(() => {
                this.resizeFrame = null;
                window.dispatchEvent(new Event("resize"));
            });
        },

        /**
         * Start dragging the list's edge
         * @param {PointerEvent} event The press
         * @returns {void}
         */
        startResize(event) {
            if (event.button !== 0) {
                return;
            }
            event.preventDefault();
            this.measure();
            this.resizing = true;
            this.dragStartX = event.clientX;
            this.dragStartWidth = this.$refs.listPanel.offsetWidth;
            event.currentTarget.setPointerCapture?.(event.pointerId);
            window.addEventListener("pointermove", this.onResizeMove);
            window.addEventListener("pointerup", this.stopResize);
            window.addEventListener("pointercancel", this.stopResize);
        },

        /**
         * Follow the pointer
         * @param {PointerEvent} event The move
         * @returns {void}
         */
        onResizeMove(event) {
            this.setWidth(this.dragStartWidth + event.clientX - this.dragStartX, false);
        },

        /**
         * Stop dragging, and remember the width
         * @returns {void}
         */
        stopResize() {
            window.removeEventListener("pointermove", this.onResizeMove);
            window.removeEventListener("pointerup", this.stopResize);
            window.removeEventListener("pointercancel", this.stopResize);
            if (this.resizing) {
                this.resizing = false;
                this.setWidth(this.listWidth);
                this.measure();
            }
        },

        /**
         * The keyboard on the list's edge
         * @param {KeyboardEvent} event The key
         * @returns {void}
         */
        onResizeKey(event) {
            const width = this.listWidth ?? this.$refs.listPanel?.offsetWidth ?? MIN_LIST_WIDTH;
            const next = keyListWidth(event.key, event.shiftKey, width, this.rowWidth);
            if (next === undefined) {
                return;
            }
            event.preventDefault();
            this.setWidth(next);
            this.measure();
        },
    },
};
</script>

<style lang="scss" scoped>
@import "../assets/vars.scss";

.container-fluid {
    width: 98%;
}

// Beside the monitor's page, the list has its own width, which its edge changes
.resizable-row {
    flex-wrap: nowrap;
}

.list-panel {
    flex: 0 0 auto;
    // The layout's own share, until it's resized
    width: calc(100% * 5 / 12);
    min-width: 280px;
    padding-right: 0;

    @media (min-width: 1200px) {
        width: calc(100% / 3);
    }

    // Text isn't selected while the edge is dragged
    &.resizing {
        user-select: none;
    }
}

.detail-panel {
    flex: 1 1 0;
    min-width: 0;
}

.panel-resizer {
    flex: 0 0 auto;
    width: 14px;
    margin: 0 2px;
    padding: 0;
    cursor: col-resize;
    touch-action: none;
    display: flex;
    justify-content: center;
    align-self: stretch;
    border-radius: 6px;

    .grip {
        width: 4px;
        margin-top: 64px;
        height: calc(100vh - 180px);
        max-height: 100%;
        border-radius: 2px;
        background-color: transparent;
        transition: background-color 0.15s;
    }

    &:hover .grip,
    &.resizing .grip,
    &:focus-visible .grip {
        background-color: rgba($primary, 0.6);
    }

    &:focus-visible {
        outline: 2px solid $primary;
        outline-offset: 1px;
    }
}
</style>
