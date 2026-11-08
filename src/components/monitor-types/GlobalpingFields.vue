<template>
    <!-- Hostname -->
    <div v-if="monitor.subtype === 'ping' || monitor.subtype === 'dns'" class="my-3">
        <label for="hostname" class="form-label">{{ $t("Hostname") }}</label>
        <input
            id="hostname"
            v-model="monitor.hostname"
            type="text"
            class="form-control"
            required
            data-testid="hostname-input"
        />
        <div class="form-text">
            {{ $t("GlobalpingHostname") }}
        </div>
    </div>

    <div v-if="monitor.subtype === 'http'" class="my-3">
        <label for="url" class="form-label">{{ $t("URL") }}</label>
        <input
            id="url"
            v-model="monitor.url"
            type="url"
            class="form-control"
            pattern="https?://.+"
            required
            data-testid="url-input"
        />
        <div class="form-text">
            {{ $t("GlobalpingHostname") }}
        </div>
    </div>

    <!-- Location -->
    <div class="my-3">
        <label for="location" class="form-label">{{ $t("Location") }}</label>
        <input id="location" v-model="monitor.location" type="text" class="form-control" required />
        <i18n-t keypath="GlobalpingLocationDescription" tag="div" class="form-text">
            <template #plus>
                <code>+</code>
            </template>
            <template #amazonPlusGermany>
                <code>amazon+germany</code>
            </template>
            <template #comcastPlusCalifornia>
                <code>comcast+california</code>
            </template>
            <template #datacenter>
                <code>+datacenter</code>
            </template>
            <template #fullDocs>
                <a
                    href="https://github.com/jsdelivr/globalping?tab=readme-ov-file#basic-location-targeting-"
                    target="_blank"
                >
                    {{ $t("GlobalpingLocationDocs") }}
                </a>
            </template>
        </i18n-t>
    </div>

    <!-- IP Family -->
    <div class="my-3">
        <label for="ipFamily" class="form-label">{{ $t("Ip Family") }}</label>
        <select id="ipFamily" v-model="monitor.ipFamily" class="form-select">
            <option :value="null">{{ $t("auto-select") }}</option>
            <option value="ipv4">IPv4</option>
            <option value="ipv6">IPv6</option>
        </select>
        <div class="form-text">
            {{ $t("GlobalpingIpFamilyInfo") }}
        </div>
    </div>

    <div v-if="monitor.subtype === 'http' || monitor.subtype === 'dns'" class="my-3">
        <label for="dns_resolve_server" class="form-label">
            {{ $t("Resolver Server") }}
        </label>
        <input id="dns_resolve_server" v-model="monitor.dns_resolve_server" type="text" class="form-control" />
        <div class="form-text">
            {{ $t("GlobalpingResolverInfo") }}
        </div>
    </div>

    <!-- DNS -->
    <template v-if="monitor.subtype === 'dns'">
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
                value="53"
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
                :options="globalpingdnsresolvetypeoptions"
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

        <div class="my-3">
            <label for="keyword" class="form-label">{{ $t("RecordMatch") }}</label>
            <input id="keyword" v-model="monitor.keyword" type="text" class="form-control" />
            <div class="form-text">
                {{ $t("RegexMatch") }}
            </div>
        </div>
    </template>

    <!-- Protocol -->
    <div class="my-3">
        <label for="protocol" class="form-label">{{ $t("Protocol") }}</label>
        <select id="protocol" v-model="monitor.protocol" class="form-select" required>
            <template v-if="monitor.subtype === 'ping'">
                <option value="ICMP">ICMP</option>
                <option value="TCP">TCP</option>
            </template>
            <template v-else-if="monitor.subtype === 'http'">
                <option :value="null">{{ $t("auto-select") }}</option>
                <option value="HTTP2">HTTP2</option>
            </template>
            <template v-else-if="monitor.subtype === 'dns'">
                <option value="UDP">UDP</option>
                <option value="TCP">TCP</option>
            </template>
        </select>
    </div>
</template>

<script>
import VueMultiselect from "vue-multiselect";

/**
 * The Edit Monitor form's fields for Globalping monitors: target, locations and options. They edit the form's monitor, which
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
        globalpingdnsresolvetypeoptions() {
            return this.editMonitor.globalpingdnsresolvetypeoptions;
        },
    },
};
</script>
