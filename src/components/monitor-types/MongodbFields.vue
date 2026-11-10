<template>
    <div class="my-3">
        <label for="mongodbCommand" class="form-label">{{ $t("Command") }}</label>
        <textarea
            id="mongodbCommand"
            v-model="monitor.databaseQuery"
            class="form-control"
            :placeholder="$t('Example:', ['{ &quot;ping&quot;: 1 }'])"
        ></textarea>
        <i18n-t tag="div" class="form-text" keypath="mongodbCommandDescription">
            <template #documentation>
                <a
                    href="https://www.mongodb.com/docs/manual/reference/command/"
                    target="_blank"
                    rel="noopener noreferrer"
                >
                    {{ $t("documentationOf", ["MongoDB"]) }}
                </a>
            </template>
        </i18n-t>
    </div>
    <div class="my-3">
        <label for="jsonPath" class="form-label">{{ $t("Json Query") }}</label>
        <input id="jsonPath" v-model="monitor.jsonPath" type="text" class="form-control" />

        <i18n-t tag="div" class="form-text" keypath="jsonQueryDescription">
            <a href="https://jsonata.org/" target="_blank" rel="noopener noreferrer">jsonata.org</a>
            <a href="https://try.jsonata.org/" target="_blank" rel="noopener noreferrer">
                {{ $t("here") }}
            </a>
        </i18n-t>
    </div>
    <div class="my-3">
        <label for="expectedValue" class="form-label">{{ $t("Expected Value") }}</label>
        <input id="expectedValue" v-model="monitor.expectedValue" type="text" class="form-control" />
    </div>
</template>

<script>
/**
 * The Edit Monitor form's fields for MongoDB monitors: command and expected value. They edit the form's monitor, which
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
