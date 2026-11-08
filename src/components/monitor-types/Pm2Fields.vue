<template>
    <div class="my-3">
        <div class="d-flex justify-content-between align-items-center">
            <label for="pm2-process-name" class="form-label mb-0">
                {{ $t("PM2 Process") }}
            </label>
            <button
                class="btn btn-outline-secondary btn-sm"
                type="button"
                :disabled="pm2ProcessLoading"
                @click="loadPM2ProcessList"
            >
                {{ pm2ProcessLoading ? $t("Loading...") : $t("Refresh") }}
            </button>
        </div>
        <select
            v-if="pm2ProcessOptions.length > 0"
            id="pm2-process-name"
            v-model="monitor.config.systemServiceName"
            class="form-select mt-2"
            required
        >
            <option disabled value="">{{ $t("Select PM2 process") }}</option>
            <!-- Save the PM2 name because PM2 can reassign numeric IDs after delete/recreate. -->
            <option v-for="item in pm2ProcessOptions" :key="`${item.name}-${item.id}`" :value="item.name">
                {{ item.name }} (#{{ item.id }}) - {{ item.status }}
            </option>
        </select>
        <input
            v-else
            id="pm2-process-name"
            v-model="monitor.config.systemServiceName"
            type="text"
            class="form-control mt-2"
            placeholder="api"
            required
        />
        <div v-if="pm2ProcessError" class="text-danger small mt-2">
            {{ pm2ProcessError }}
        </div>

        <div class="form-text">
            {{
                $t("pm2Description", {
                    service_name: monitor.config.systemServiceName || "api",
                })
            }}
            <div class="mt-2">
                <i18n-t keypath="systemServiceCommandHint" tag="span">
                    <template #command>
                        <code>pm2 jlist</code>
                    </template>
                </i18n-t>
            </div>
            <div class="text-secondary small">
                {{ $t("pm2ExpectedStates") }}
            </div>
        </div>
    </div>
</template>

<script>
/**
 * The Edit Monitor form's fields for PM2 monitors: the process. They edit the form's monitor, which
 * EditMonitor provides.
 */
export default {
    inject: ["editMonitor"],
    computed: {
        monitor() {
            return this.editMonitor.monitor;
        },
        pm2ProcessError() {
            return this.editMonitor.pm2ProcessError;
        },
        pm2ProcessLoading() {
            return this.editMonitor.pm2ProcessLoading;
        },
        pm2ProcessOptions() {
            return this.editMonitor.pm2ProcessOptions;
        },
    },
    methods: {
        loadPM2ProcessList(...args) {
            return this.editMonitor.loadPM2ProcessList(...args);
        },
    },
};
</script>
