<template>
    <!-- Kafka Brokers List -->
    <div class="my-3">
        <label for="kafkaProducerBrokers" class="form-label">
            {{ $t("Kafka Brokers") }}
        </label>
        <VueMultiselect
            id="kafkaProducerBrokers"
            v-model="monitor.kafkaProducerBrokers"
            :multiple="true"
            :options="[]"
            :placeholder="$t('Enter the list of brokers')"
            :tag-placeholder="$t('Press Enter to add broker')"
            :max-height="500"
            :taggable="true"
            :show-no-options="false"
            :close-on-select="false"
            :clear-on-select="false"
            :preserve-search="false"
            :preselect-first="false"
            @tag="addKafkaProducerBroker"
        ></VueMultiselect>
    </div>

    <!-- Kafka Topic Name -->
    <div class="my-3">
        <label for="kafkaProducerTopic" class="form-label">
            {{ $t("Kafka Topic Name") }}
        </label>
        <input id="kafkaProducerTopic" v-model="monitor.kafkaProducerTopic" type="text" class="form-control" required />
    </div>

    <!-- Kafka Producer Message -->
    <div class="my-3">
        <label for="kafkaProducerMessage" class="form-label">
            {{ $t("Kafka Producer Message") }}
        </label>
        <input
            id="kafkaProducerMessage"
            v-model="monitor.kafkaProducerMessage"
            type="text"
            class="form-control"
            required
        />
    </div>

    <!-- Kafka SSL -->
    <div class="my-3 form-check">
        <input id="kafkaProducerSsl" v-model="monitor.kafkaProducerSsl" class="form-check-input" type="checkbox" />
        <label class="form-check-label" for="kafkaProducerSsl">
            {{ $t("Enable Kafka SSL") }}
        </label>
    </div>

    <!-- Kafka SSL -->
    <div class="my-3 form-check">
        <input
            id="kafkaProducerAllowAutoTopicCreation"
            v-model="monitor.kafkaProducerAllowAutoTopicCreation"
            class="form-check-input"
            type="checkbox"
        />
        <label class="form-check-label" for="kafkaProducerAllowAutoTopicCreation">
            {{ $t("Enable Kafka Producer Auto Topic Creation") }}
        </label>
    </div>
</template>

<script>
import VueMultiselect from "vue-multiselect";

/**
 * The Edit Monitor form's fields for Kafka producer monitors: brokers, topic and message. They edit the form's monitor, which
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
    },
    methods: {
        addKafkaProducerBroker(...args) {
            return this.editMonitor.addKafkaProducerBroker(...args);
        },
    },
};
</script>
