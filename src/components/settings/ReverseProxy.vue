<template>
    <div>
        <h4 class="mt-4">Cloudflare Tunnel</h4>

        <div id="cloudflare-tunnel" class="my-3">
            <p>{{ $t("cloudflaredSidecar") }}</p>
            <i18n-t tag="p" keypath="cloudflaredSidecarHow">
                <template #profile>
                    <code>docker compose --profile tunnel up -d</code>
                </template>
                <template #token>
                    <code>CLOUDFLARE_TUNNEL_TOKEN</code>
                </template>
                <template #helm>
                    <code>cloudflared.enabled</code>
                </template>
            </i18n-t>
            <a
                href="https://github.com/pantheon-lab/uptime-kuma/tree/main/server-go#cloudflare-tunnel"
                target="_blank"
                rel="noopener"
            >
                {{ $t("cloudflaredSidecarGuide") }}
            </a>
        </div>

        <h4 class="mt-4">{{ $t("Other Software") }}</h4>
        <div>
            {{ $t("For example: nginx, Apache and Traefik.") }}
            <br />
            <a
                href="https://github.com/pantheon-lab/uptime-kuma/tree/main/server-go#behind-a-reverse-proxy"
                target="_blank"
                rel="noopener"
            >
                {{ $t("reverseProxyGuideLink") }}
            </a>
        </div>

        <h4 class="my-4">{{ $t("HTTP Headers") }}</h4>
        <div class="my-3">
            <label class="form-label">
                {{ $t("Trust Proxy") }}
            </label>
            <div class="form-check">
                <input
                    id="trustProxyYes"
                    v-model="settings.trustProxy"
                    class="form-check-input"
                    type="radio"
                    name="trustProxyYes"
                    :value="true"
                    required
                />
                <label class="form-check-label" for="trustProxyYes">
                    {{ $t("Yes") }}
                </label>
            </div>
            <div class="form-check">
                <input
                    id="trustProxyNo"
                    v-model="settings.trustProxy"
                    class="form-check-input"
                    type="radio"
                    name="flexRadioDefault"
                    :value="false"
                    required
                />
                <label class="form-check-label" for="trustProxyNo">
                    {{ $t("No") }}
                </label>
            </div>

            <div class="form-text">
                {{ $t("trustProxyDescription") }}
            </div>
        </div>

        <div>
            <button class="btn btn-primary" type="submit" @click="saveSettings()">
                {{ $t("Save") }}
            </button>
        </div>
    </div>
</template>

<script>
export default {
    computed: {
        settings() {
            return this.$parent.$parent.$parent.settings;
        },
        saveSettings() {
            return this.$parent.$parent.$parent.saveSettings;
        },
    },
};
</script>
