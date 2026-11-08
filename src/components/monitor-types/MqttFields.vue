<template>
    <div class="my-3">
        <label for="mqttUsername" class="form-label">MQTT {{ $t("Username") }}</label>
        <input id="mqttUsername" v-model="monitor.mqttUsername" type="text" class="form-control" />
    </div>

    <div class="my-3">
        <label for="mqttPassword" class="form-label">MQTT {{ $t("Password") }}</label>
        <HiddenInput id="mqttPassword" v-model="monitor.mqttPassword" autocomplete="new-password" />
    </div>

    <div class="my-3">
        <label for="mqttTopic" class="form-label">MQTT {{ $t("Topic") }}</label>
        <input id="mqttTopic" v-model="monitor.mqttTopic" type="text" class="form-control" required />
        <div class="form-text">
            {{ $t("topicExplanation") }}
        </div>
    </div>

    <div class="my-3">
        <label for="mqttWebsocketPath" class="form-label">
            {{ $t("mqttWebSocketPath") }}
        </label>
        <input
            v-if="/(mqtts?|wss?):\/\/.+/.test(monitor.hostname)"
            id="mqttWebsocketPath"
            v-model="monitor.mqttWebsocketPath"
            type="text"
            class="form-control"
        />
        <input v-else type="text" class="form-control" disabled />
        <div class="form-text">
            {{ $t("mqttWebsocketPathExplanation") }}
        </div>
    </div>

    <div class="my-3">
        <label for="mqttCheckType" class="form-label">MQTT {{ $t("Check Type") }}</label>
        <select id="mqttCheckType" v-model="monitor.mqttCheckType" class="form-select" required>
            <option value="keyword">{{ $t("Keyword") }}</option>
            <option value="json-query">{{ $t("Json Query") }}</option>
        </select>
    </div>

    <div v-if="monitor.mqttCheckType === 'keyword'" class="my-3">
        <label for="mqttSuccessKeyword" class="form-label">MQTT {{ $t("successKeyword") }}</label>
        <input id="mqttSuccessKeyword" v-model="monitor.mqttSuccessMessage" type="text" class="form-control" />
        <div class="form-text">
            {{ $t("successKeywordExplanation") }}
        </div>
    </div>

    <!-- Json Query -->
    <div v-if="monitor.mqttCheckType === 'json-query'" class="my-3">
        <label for="jsonPath" class="form-label">{{ $t("Json Query") }}</label>
        <input id="jsonPath" v-model="monitor.jsonPath" type="text" class="form-control" required />

        <i18n-t tag="div" class="form-text" keypath="jsonQueryDescription">
            <a href="https://jsonata.org/" target="_blank" rel="noopener noreferrer">jsonata.org</a>
            <a href="https://try.jsonata.org/" target="_blank" rel="noopener noreferrer">
                {{ $t("here") }}
            </a>
        </i18n-t>
        <br />

        <label for="expectedValue" class="form-label">{{ $t("Expected Value") }}</label>
        <input id="expectedValue" v-model="monitor.expectedValue" type="text" class="form-control" required />
    </div>
</template>

<script>
import HiddenInput from "../HiddenInput.vue";

/**
 * The Edit Monitor form's fields for MQTT monitors: topic, login and what to expect. They edit the form's monitor, which
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
