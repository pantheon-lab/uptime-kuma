<template>
    <section
        class="shadow-box appearance-preview"
        :aria-label="$t('appearancePreview')"
        data-testid="appearance-preview"
    >
        <h6 class="preview-title">{{ $t("appearancePreview") }}</h6>

        <div class="preview-label">{{ $t("appearancePreviewList") }}</div>
        <!-- As the dashboard's monitor list draws a row, with made-up monitors and beats -->
        <div class="monitor-list preview-list" inert>
            <div v-for="row in rows" :key="row.id" class="draggable-item">
                <div class="item" :class="{ disabled: !row.active }">
                    <div class="row">
                        <div class="small-padding d-flex gap-2 align-items-center" :class="monitorStyle">
                            <div class="me-1">
                                <span class="badge rounded-pill" :class="`bg-${row.color}`">
                                    <font-awesome-icon v-if="row.icon" :icon="row.icon" class="status-icon" />
                                    {{ row.uptime }}
                                </span>
                            </div>
                            <div class="flex-fill text-truncate">{{ row.name }}</div>
                        </div>
                        <div v-if="$root.userHeartbeatBar === 'normal'" :key="barKey" class="col-3 col-xl-6">
                            <HeartbeatBar size="small" :monitor-id="row.id" :heartbeat-list="row.beats" />
                        </div>
                    </div>
                    <div v-if="$root.userHeartbeatBar === 'bottom'" :key="barKey" class="row">
                        <div class="col-12 bottom-style">
                            <HeartbeatBar size="small" :monitor-id="row.id" :heartbeat-list="row.beats" />
                        </div>
                    </div>
                </div>
            </div>
        </div>

        <div class="preview-label mt-3">{{ $t("appearancePreviewPage") }}</div>
        <div>
            <HeartbeatBar :key="barKey" :monitor-id="rows[1].id" :heartbeat-list="rows[1].beats" />
            <div v-if="$root.isMobile" class="form-text">{{ $t("appearancePreviewMobile") }}</div>
        </div>
    </section>
</template>

<script>
import HeartbeatBar from "../HeartbeatBar.vue";
import { previewBeats } from "../../appearance-preview";
import { DOWN, MAINTENANCE, PENDING } from "../../util.ts";

// Made-up monitors: ids no real monitor has, so the bars never ask the server for history
const PREVIEW_IDS = [-1, -2, -3];

/**
 * What the Appearance settings change, drawn with made-up monitors: the list's heartbeat bars,
 * and a monitor page's bar with its elapsed time. The theme applies to the whole page already.
 */
export default {
    components: {
        HeartbeatBar,
    },
    data() {
        // A little before now, as the newest beat of a real monitor would be
        const now = new Date(Date.now() - 20_000);
        const makeBeats = (count, statuses) => previewBeats(now, count, statuses, this.$t("appearancePreviewDownMsg"));
        return {
            rows: [
                {
                    id: PREVIEW_IDS[0],
                    name: this.$t("appearancePreviewWebsite"),
                    active: true,
                    color: "primary",
                    icon: null,
                    uptime: "100%",
                    beats: makeBeats(90, {}),
                },
                {
                    id: PREVIEW_IDS[1],
                    name: this.$t("appearancePreviewApi"),
                    active: true,
                    color: "danger",
                    icon: "times-circle",
                    uptime: "96.7%",
                    beats: makeBeats(90, { 0: DOWN, 1: DOWN, 2: PENDING, 25: DOWN, 50: MAINTENANCE, 51: MAINTENANCE }),
                },
                {
                    id: PREVIEW_IDS[2],
                    name: this.$t("appearancePreviewPaused"),
                    active: false,
                    color: "secondary",
                    icon: null,
                    uptime: "99.2%",
                    beats: makeBeats(90, { 40: DOWN }),
                },
            ],
        };
    },
    computed: {
        // The list's rows are as wide as the heartbeat bar setting leaves them
        monitorStyle() {
            return this.$root.userHeartbeatBar === "normal" ? { "col-9": true, "col-xl-6": true } : {};
        },
        // Draw the bars again when a setting changes their size or the theme their colours
        barKey() {
            return `${this.$root.userHeartbeatBar}-${this.$root.theme}`;
        },
    },
};
</script>

<style lang="scss" scoped>
@import "../../assets/vars.scss";

.appearance-preview {
    padding: 16px;
}

.preview-title {
    margin-bottom: 12px;
}

.preview-label {
    font-size: 13px;
    color: $secondary-text;
    margin-bottom: 6px;
}

.preview-list {
    pointer-events: none;

    .item {
        padding: 12px 15px;
    }
}

.small-padding {
    padding-left: 5px !important;
    padding-right: 5px !important;
}

.bottom-style {
    margin-left: -10px;
    margin-top: 5px;
}

.badge .status-icon {
    margin-right: 3px;
}
</style>
