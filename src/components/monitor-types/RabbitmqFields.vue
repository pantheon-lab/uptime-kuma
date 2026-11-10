<template>
    <!-- RabbitMQ Nodes List -->
    <div class="my-3">
        <label for="rabbitmqNodes" class="form-label">{{ $t("RabbitMQ Nodes") }}</label>
        <VueMultiselect
            id="rabbitmqNodes"
            v-model="monitor.rabbitmqNodes"
            :required="true"
            :multiple="true"
            :options="[]"
            :placeholder="$t('Enter the list of nodes')"
            :tag-placeholder="$t('Press Enter to add node')"
            :max-height="500"
            :taggable="true"
            :show-no-options="false"
            :close-on-select="false"
            :clear-on-select="false"
            :preserve-search="false"
            :preselect-first="false"
            @tag="addRabbitmqNode"
        ></VueMultiselect>
        <div class="form-text">
            {{ $t("rabbitmqNodesDescription", ["https://node1.rabbitmq.com:15672"]) }}
        </div>
    </div>

    <div class="my-3">
        <label for="rabbitmqUsername" class="form-label">RabbitMQ {{ $t("RabbitMQ Username") }}</label>
        <input id="rabbitmqUsername" v-model="monitor.rabbitmqUsername" type="text" required class="form-control" />
    </div>

    <div class="my-3">
        <label for="rabbitmqPassword" class="form-label">
            {{ $t("RabbitMQ Password") }}
        </label>
        <HiddenInput
            id="rabbitmqPassword"
            v-model="monitor.rabbitmqPassword"
            autocomplete="false"
            required="true"
        ></HiddenInput>
    </div>
</template>

<script>
import HiddenInput from "../HiddenInput.vue";
import VueMultiselect from "vue-multiselect";

/**
 * The Edit Monitor form's fields for RabbitMQ monitors: nodes and login. They edit the form's monitor, which
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
    },
    methods: {
        addRabbitmqNode(...args) {
            return this.editMonitor.addRabbitmqNode(...args);
        },
    },
};
</script>
