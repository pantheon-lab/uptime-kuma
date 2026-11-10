<template>
    <section
        v-if="state === 'active'"
        class="shadow-box big-padding mb-3 getting-started"
        data-testid="getting-started"
        aria-labelledby="getting-started-title"
    >
        <div class="guide-header">
            <h2 id="getting-started-title" class="h5 mb-0 me-auto">{{ $t("gettingStartedTitle") }}</h2>
            <span class="form-text me-2" data-testid="getting-started-progress">
                {{ $t("gettingStartedProgress", [doneCount, steps.length]) }}
            </span>
            <button
                type="button"
                class="btn btn-sm btn-outline-normal"
                data-testid="getting-started-hide"
                @click="hide"
            >
                {{ $t("gettingStartedHide") }}
            </button>
        </div>
        <p class="form-text mt-1 mb-3">{{ $t("gettingStartedIntro") }}</p>

        <ol class="guide-steps">
            <li
                v-for="(step, i) in steps"
                :key="step.key"
                class="guide-step"
                :class="{ done: step.done, next: i === nextIndex }"
                :data-testid="`getting-started-${step.key}`"
                :data-done="step.done"
            >
                <span class="step-mark" aria-hidden="true">
                    <font-awesome-icon v-if="step.done" icon="check" />
                    <template v-else>{{ i + 1 }}</template>
                </span>
                <div class="step-text">
                    <strong>{{ $t(`gettingStarted-${step.key}`) }}</strong>
                    <span class="visually-hidden">
                        {{ step.done ? $t("gettingStartedDone") : $t("gettingStartedToDo") }}
                    </span>
                    <div class="form-text">{{ $t(`gettingStarted-${step.key}-help`) }}</div>
                </div>
                <div class="step-actions">
                    <template v-if="step.key === 'monitor'">
                        <router-link to="/add" class="btn btn-sm" :class="buttonClass(step, i)">
                            {{ $t("Add New Monitor") }}
                        </router-link>
                        <router-link to="/add?import=1" class="btn btn-sm btn-outline-normal">
                            {{ $t("importMonitorsButton") }}
                        </router-link>
                    </template>
                    <router-link
                        v-else-if="step.key === 'notification'"
                        to="/settings/notifications"
                        class="btn btn-sm"
                        :class="buttonClass(step, i)"
                    >
                        {{ $t("Setup Notification") }}
                    </router-link>
                    <router-link v-else to="/add-status-page" class="btn btn-sm" :class="buttonClass(step, i)">
                        {{ $t("Add New Status Page") }}
                    </router-link>
                </div>
            </li>
        </ol>
    </section>
</template>

<script>
import { GUIDE_KEY, guideSteps, loadGuideState, nextGuideState } from "../getting-started";

/**
 * The first run's guide on the dashboard, for admins of a server that had no monitors.
 */
export default {
    data() {
        return {
            saved: loadGuideState(globalThis.localStorage),
        };
    },
    computed: {
        counts() {
            return {
                monitors: Object.keys(this.$root.monitorList).length,
                notifications: this.$root.notificationList.length,
                statusPages: Object.keys(this.$root.statusPageList ?? {}).length,
            };
        },
        steps() {
            return guideSteps(this.counts);
        },
        doneCount() {
            return this.steps.filter((s) => s.done).length;
        },
        nextIndex() {
            return this.steps.findIndex((s) => !s.done);
        },
        ready() {
            return this.$root.canEdit && this.$root.monitorListLoaded && this.$root.statusPageListLoaded;
        },
        state() {
            return this.ready ? nextGuideState(this.saved, this.counts) : "";
        },
    },
    watch: {
        // Also at first: the last step may have been done away from the dashboard
        state: {
            handler(state) {
                if (state && state !== this.saved) {
                    this.save(state);
                }
            },
            immediate: true,
        },
    },
    methods: {
        /**
         * The next step's button stands out
         * @param {object} step The step
         * @param {number} i Its place
         * @returns {string} Classes
         */
        buttonClass(step, i) {
            return i === this.nextIndex ? "btn-primary" : "btn-outline-normal";
        },

        /**
         * Keep the state in this browser
         * @param {string} state The state
         * @returns {void}
         */
        save(state) {
            this.saved = state;
            try {
                localStorage.setItem(GUIDE_KEY, state);
            } catch {
                // Storage blocked: the guide shows again next time
            }
        },

        /**
         * Hide the guide for good, in this browser
         * @returns {void}
         */
        hide() {
            this.save("hidden");
        },
    },
};
</script>

<style lang="scss" scoped>
@import "../assets/vars";

.guide-header {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
}

.guide-steps {
    list-style: none;
    margin: 0;
    padding: 0;
}

.guide-step {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 12px;
    padding: 10px 0;
    border-top: 1px solid rgba(128, 128, 128, 0.15);

    .step-text {
        flex: 1 1 14rem;
    }

    .step-actions {
        display: flex;
        gap: 6px;
    }

    &.done .step-text strong {
        text-decoration: line-through;
        opacity: 0.6;
    }
}

.step-mark {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
    width: 28px;
    height: 28px;
    border-radius: 50%;
    font-weight: 600;
    border: 2px solid rgba(128, 128, 128, 0.4);

    .next & {
        border-color: $primary;
        color: $primary;
    }

    .done & {
        border-color: $primary;
        background-color: $primary;
        color: #fff;
    }
}
</style>
