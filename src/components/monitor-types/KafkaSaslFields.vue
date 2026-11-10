<template>
    <h2 class="mt-5 mb-2">{{ $t("Kafka SASL Options") }}</h2>
    <div class="my-3">
        <label class="form-label" for="kafkaProducerSaslMechanism">
            {{ $t("Mechanism") }}
        </label>
        <VueMultiselect
            id="kafkaProducerSaslMechanism"
            v-model="monitor.kafkaProducerSaslOptions.mechanism"
            :options="kafkaSaslMechanismOptions"
            :multiple="false"
            :clear-on-select="false"
            :preserve-search="false"
            :placeholder="$t('Pick a SASL Mechanism...')"
            :preselect-first="false"
            :max-height="500"
            :allow-empty="false"
            :taggable="false"
        ></VueMultiselect>
    </div>
    <div v-if="monitor.kafkaProducerSaslOptions.mechanism !== 'None'">
        <div v-if="monitor.kafkaProducerSaslOptions.mechanism !== 'aws'" class="my-3">
            <label for="kafkaProducerSaslUsername" class="form-label">
                {{ $t("Username") }}
            </label>
            <input
                id="kafkaProducerSaslUsername"
                v-model="monitor.kafkaProducerSaslOptions.username"
                type="text"
                autocomplete="kafkaProducerSaslUsername"
                class="form-control"
            />
        </div>
        <div v-if="monitor.kafkaProducerSaslOptions.mechanism !== 'aws'" class="my-3">
            <label for="kafkaProducerSaslPassword" class="form-label">
                {{ $t("Password") }}
            </label>
            <HiddenInput
                id="kafkaProducerSaslPassword"
                v-model="monitor.kafkaProducerSaslOptions.password"
                autocomplete="kafkaProducerSaslPassword"
            />
        </div>
        <div v-if="monitor.kafkaProducerSaslOptions.mechanism === 'aws'" class="my-3">
            <label for="kafkaProducerSaslAuthorizationIdentity" class="form-label">
                {{ $t("Authorization Identity") }}
            </label>
            <input
                id="kafkaProducerSaslAuthorizationIdentity"
                v-model="monitor.kafkaProducerSaslOptions.authorizationIdentity"
                type="text"
                autocomplete="kafkaProducerSaslAuthorizationIdentity"
                class="form-control"
                required
            />
        </div>
        <div v-if="monitor.kafkaProducerSaslOptions.mechanism === 'aws'" class="my-3">
            <label for="kafkaProducerSaslAccessKeyId" class="form-label">
                {{ $t("AccessKey Id") }}
            </label>
            <input
                id="kafkaProducerSaslAccessKeyId"
                v-model="monitor.kafkaProducerSaslOptions.accessKeyId"
                type="text"
                autocomplete="kafkaProducerSaslAccessKeyId"
                class="form-control"
                required
            />
        </div>
        <div v-if="monitor.kafkaProducerSaslOptions.mechanism === 'aws'" class="my-3">
            <label for="kafkaProducerSaslSecretAccessKey" class="form-label">
                {{ $t("Secret AccessKey") }}
            </label>
            <HiddenInput
                id="kafkaProducerSaslSecretAccessKey"
                v-model="monitor.kafkaProducerSaslOptions.secretAccessKey"
                autocomplete="kafkaProducerSaslSecretAccessKey"
                :required="true"
            />
        </div>
        <div v-if="monitor.kafkaProducerSaslOptions.mechanism === 'aws'" class="my-3">
            <label for="kafkaProducerSaslSessionToken" class="form-label">
                {{ $t("Session Token") }}
            </label>
            <HiddenInput
                id="kafkaProducerSaslSessionToken"
                v-model="monitor.kafkaProducerSaslOptions.sessionToken"
                autocomplete="kafkaProducerSaslSessionToken"
            />
        </div>
    </div>
</template>

<script>
import HiddenInput from "../HiddenInput.vue";
import VueMultiselect from "vue-multiselect";

/**
 * The Edit Monitor form's fields for Kafka producer monitors: SASL options. They edit the form's monitor, which
 * EditMonitor provides.
 */
export default {
    components: {
        HiddenInput,
        VueMultiselect,
    },
    inject: ["editMonitor"],
    computed: {
        monitor() {
            return this.editMonitor.monitor;
        },
        kafkaSaslMechanismOptions() {
            return this.editMonitor.kafkaSaslMechanismOptions;
        },
    },
};
</script>
