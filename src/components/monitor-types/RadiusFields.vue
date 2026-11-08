<template>
    <div class="my-3">
        <label for="radius_username" class="form-label">Radius {{ $t("Username") }}</label>
        <input id="radius_username" v-model="monitor.radiusUsername" type="text" class="form-control" required />
    </div>

    <div class="my-3">
        <label for="radius_password" class="form-label">Radius {{ $t("Password") }}</label>
        <HiddenInput
            id="radius_password"
            v-model="monitor.radiusPassword"
            autocomplete="new-password"
            :required="true"
        />
    </div>

    <div class="my-3">
        <label for="radius_secret" class="form-label">{{ $t("RadiusSecret") }}</label>
        <HiddenInput id="radius_secret" v-model="monitor.radiusSecret" autocomplete="new-password" :required="true" />
        <div class="form-text">{{ $t("RadiusSecretDescription") }}</div>
    </div>

    <div class="my-3">
        <label for="radius_called_station_id" class="form-label">
            {{ $t("RadiusCalledStationId") }}
        </label>
        <input
            id="radius_called_station_id"
            v-model="monitor.radiusCalledStationId"
            type="text"
            class="form-control"
            required
        />
        <div class="form-text">{{ $t("RadiusCalledStationIdDescription") }}</div>
    </div>

    <div class="my-3">
        <label for="radius_calling_station_id" class="form-label">
            {{ $t("RadiusCallingStationId") }}
        </label>
        <input
            id="radius_calling_station_id"
            v-model="monitor.radiusCallingStationId"
            type="text"
            class="form-control"
            required
        />
        <div class="form-text">{{ $t("RadiusCallingStationIdDescription") }}</div>
    </div>
</template>

<script>
import HiddenInput from "../HiddenInput.vue";

/**
 * The Edit Monitor form's fields for RADIUS monitors: login, secret and station ids. They edit the form's monitor, which
 * EditMonitor provides.
 */
export default {
    components: {
        HiddenInput,
    },
    inject: ["editMonitor"],
    computed: {
        monitor() {
            return this.editMonitor.monitor;
        },
    },
};
</script>
