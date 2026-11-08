<template>
    <h4 class="mt-4">{{ $t("ntpThresholdsTitle") }}</h4>

    <div class="my-3">
        <label for="ntp-stratum-threshold" class="form-label">
            {{ $t("ntpStratumThreshold") }}
        </label>
        <input
            id="ntp-stratum-threshold"
            v-model.number="monitor.ntpStratumThreshold"
            type="number"
            class="form-control"
            min="1"
            max="15"
            placeholder="5"
        />
        <div class="form-text">
            {{ $t("ntpStratumThresholdHelp") }}
        </div>
    </div>

    <div class="my-3">
        <label for="ntp-offset-threshold" class="form-label">
            {{ $t("ntpOffsetThreshold") }}
        </label>
        <input
            id="ntp-offset-threshold"
            v-model.number="monitor.ntpTimeOffsetThreshold"
            type="number"
            class="form-control"
            min="1"
            placeholder="1000"
        />
        <div class="form-text">
            {{ $t("ntpOffsetThresholdHelp") }}
        </div>
    </div>

    <div class="my-3">
        <label for="ntp-dispersion-threshold" class="form-label">
            {{ $t("ntpDispersionThreshold") }}
        </label>
        <input
            id="ntp-dispersion-threshold"
            v-model.number="monitor.ntpRootDispersionThreshold"
            type="number"
            class="form-control"
            min="1"
            placeholder="500"
        />
        <div class="form-text">
            {{ $t("ntpDispersionThresholdHelp") }}
        </div>
    </div>
</template>

<script>
/**
 * The Edit Monitor form's fields for NTP monitors: thresholds. They edit the form's monitor, which
 * EditMonitor provides.
 */
export default {
    inject: ["editMonitor"],
    computed: {
        monitor() {
            return this.editMonitor.monitor;
        },
    },
};
</script>
