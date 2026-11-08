<template>
    <div class="my-3">
        <label for="dns_resolve_server" class="form-label">
            {{ $t("Resolver Server(s)") }}
        </label>
        <input id="dns_resolve_server" v-model="monitor.dns_resolve_server" type="text" class="form-control" required />
        <div class="form-text">
            {{ $t("resolverserverDescription") }}
        </div>
    </div>

    <!-- Port -->
    <div class="my-3">
        <label for="port" class="form-label">{{ $t("Port") }}</label>
        <input
            id="port"
            v-model="monitor.port"
            type="number"
            class="form-control"
            required
            min="0"
            max="65535"
            step="1"
        />
        <div class="form-text">
            {{ $t("dnsPortDescription") }}
        </div>
    </div>

    <div class="my-3">
        <label for="dns_resolve_type" class="form-label">
            {{ $t("Resource Record Type") }}
        </label>

        <!-- :allow-empty="false" is not working, set a default value instead https://github.com/shentao/vue-multiselect/issues/336   -->
        <VueMultiselect
            id="dns_resolve_type"
            v-model="monitor.dns_resolve_type"
            :options="dnsresolvetypeOptions"
            :multiple="false"
            :close-on-select="true"
            :clear-on-select="false"
            :preserve-search="false"
            :placeholder="$t('Pick a RR-Type...')"
            :preselect-first="false"
            :max-height="500"
            :taggable="false"
            data-testid="resolve-type-select"
        ></VueMultiselect>

        <div class="form-text">
            {{ $t("rrtypeDescription") }}
        </div>
    </div>
</template>

<script>
import VueMultiselect from "vue-multiselect";

/**
 * The Edit Monitor form's fields for DNS monitors: record type, resolvers and port. They edit the form's monitor, which
 * EditMonitor provides.
 */
export default {
    components: {
        VueMultiselect,
    },
    inject: ["editMonitor"],
    computed: {
        monitor() {
            return this.editMonitor.monitor;
        },
        dnsresolvetypeOptions() {
            return this.editMonitor.dnsresolvetypeOptions;
        },
    },
};
</script>
