<template>
    <div
        ref="modal"
        class="modal fade"
        tabindex="-1"
        aria-labelledby="import-monitors-title"
        data-testid="import-dialog"
    >
        <div class="modal-dialog modal-lg modal-dialog-scrollable">
            <div class="modal-content">
                <div class="modal-header">
                    <h5 id="import-monitors-title" class="modal-title">{{ $t("importMonitorsTitle") }}</h5>
                    <button type="button" class="btn-close" data-bs-dismiss="modal" :aria-label="$t('Close')" />
                </div>
                <div class="modal-body">
                    <p class="mt-0 mb-2">{{ $t("importMonitorsHelp") }}</p>
                    <details class="mb-3">
                        <summary>{{ $t("importMonitorsExamples") }}</summary>
                        <pre class="example">{{ csvExample }}</pre>
                        <pre class="example">{{ yamlExample }}</pre>
                        <p class="form-text mb-0">{{ $t("importMonitorsFields") }}</p>
                    </details>

                    <div class="mb-2">
                        <label for="import-file" class="form-label">{{ $t("importMonitorsFile") }}</label>
                        <input
                            id="import-file"
                            type="file"
                            class="form-control"
                            accept=".csv,.yaml,.yml,.json,text/csv,application/yaml,application/json"
                            data-testid="import-file"
                            @change="readFile"
                        />
                    </div>
                    <label for="import-text" class="form-label">{{ $t("importMonitorsPaste") }}</label>
                    <textarea
                        id="import-text"
                        v-model="text"
                        class="form-control font-monospace"
                        rows="8"
                        spellcheck="false"
                        data-testid="import-text"
                        @input="onEdit"
                    ></textarea>
                    <p class="form-text mb-2">
                        {{ $t("importMonitorsFormat", [format === "csv" ? "CSV" : "YAML"]) }}
                    </p>

                    <div
                        v-if="message"
                        class="alert py-2"
                        :class="messageOk ? 'alert-success' : 'alert-danger'"
                        role="status"
                        data-testid="import-message"
                    >
                        {{ message }}
                    </div>
                    <ul v-if="localErrors.length" class="import-errors" data-testid="import-errors">
                        <li v-for="e in localErrors" :key="e">{{ e }}</li>
                    </ul>

                    <table v-if="rows.length" class="table table-sm import-rows" data-testid="import-rows">
                        <thead>
                            <tr>
                                <th>#</th>
                                <th>{{ $t("Friendly Name") }}</th>
                                <th>{{ $t("Monitor Type") }}</th>
                                <th>{{ $t("importMonitorsResult") }}</th>
                            </tr>
                        </thead>
                        <tbody>
                            <tr
                                v-for="r in rows"
                                :key="r.row"
                                :class="{ 'table-danger': r.error }"
                                data-testid="import-row"
                            >
                                <td>{{ r.row }}</td>
                                <td>{{ r.name }}</td>
                                <td>{{ r.type }}</td>
                                <td>
                                    <span v-if="r.error" class="text-danger">{{ r.error }}</span>
                                    <router-link v-else-if="r.id" :to="`/dashboard/${r.id}`" @click="modal.hide()">
                                        {{ $t("importMonitorsAdded") }}
                                    </router-link>
                                    <span v-else class="text-success">{{ $t("importMonitorsReady") }}</span>
                                </td>
                            </tr>
                        </tbody>
                    </table>
                </div>
                <div class="modal-footer">
                    <button type="button" class="btn btn-outline-normal" data-bs-dismiss="modal">
                        {{ $t("Close") }}
                    </button>
                    <button
                        type="button"
                        class="btn btn-outline-primary"
                        :disabled="processing || !text.trim()"
                        data-testid="import-check"
                        @click="send(true)"
                    >
                        {{ $t("importMonitorsCheck") }}
                    </button>
                    <button
                        type="button"
                        class="btn btn-primary"
                        :disabled="processing || !checked"
                        data-testid="import-run"
                        @click="send(false)"
                    >
                        {{ $t("importMonitorsRun", checkedCount) }}
                    </button>
                </div>
            </div>
        </div>
    </div>
</template>

<script>
import { Modal } from "bootstrap";
import { load } from "js-yaml";
import { csvToMonitors, detectFormat, yamlToMonitors } from "../monitor-import";

/**
 * Adds monitors from a CSV or YAML file, in the REST API's monitor format. The server checks every
 * monitor first, and adds none of them unless all are valid.
 */
export default {
    data() {
        return {
            modal: null,
            text: "",
            fileName: "",
            rows: [],
            localErrors: [],
            message: "",
            messageOk: false,
            // The text the server checked, so that an edit needs another check
            checkedText: null,
            checkedCount: 0,
            processing: false,
            csvExample:
                "name,type,url,interval,hostname,port,acceptedStatusCodes\n" +
                "Shop,http,https://shop.example.com,60,,,200-299;301\n" +
                "Database,port,,30,db.internal,5432,",
            yamlExample:
                "- name: Shop\n  type: keyword\n  url: https://shop.example.com\n  keyword: Welcome\n" +
                "- name: Database\n  type: port\n  hostname: db.internal\n  port: 5432\n  parentId: 3",
        };
    },
    computed: {
        format() {
            return detectFormat(this.fileName, this.text);
        },
        checked() {
            return this.checkedText !== null && this.checkedText === this.text && this.checkedCount > 0;
        },
    },
    mounted() {
        this.modal = new Modal(this.$refs.modal);
    },
    beforeUnmount() {
        this.modal?.hide();
        this.modal?.dispose();
    },
    methods: {
        /**
         * Show the dialog, empty
         * @returns {void}
         */
        show() {
            this.text = "";
            this.fileName = "";
            this.reset();
            this.modal.show();
        },

        /**
         * Forget the last check and its result
         * @returns {void}
         */
        reset() {
            this.rows = [];
            this.localErrors = [];
            this.message = "";
            this.checkedText = null;
            this.checkedCount = 0;
        },

        /**
         * The text changed: what was checked no longer holds
         * @returns {void}
         */
        onEdit() {
            if (this.checkedText !== null) {
                this.reset();
            }
        },

        /**
         * Read the chosen file into the text box
         * @param {Event} e The input's change
         * @returns {Promise<void>}
         */
        async readFile(e) {
            const file = e.target.files?.[0];
            if (!file) {
                return;
            }
            this.fileName = file.name;
            this.text = await file.text();
            this.reset();
        },

        /**
         * The monitors in the text, or null after showing what's wrong with it
         * @returns {object[]|null} Monitors
         */
        parse() {
            const parsed = this.format === "csv" ? csvToMonitors(this.text) : yamlToMonitors(this.text, (t) => load(t));
            this.localErrors = parsed.errors;
            if (parsed.errors.length) {
                this.message = this.$t("importMonitorsFileErrors");
                this.messageOk = false;
                return null;
            }
            return parsed.monitors;
        },

        /**
         * Have the server check the monitors, or add them
         * @param {boolean} dryRun Only check them
         * @returns {void}
         */
        send(dryRun) {
            this.rows = [];
            this.message = "";
            const monitors = this.parse();
            if (!monitors) {
                return;
            }
            const text = this.text;
            this.processing = true;
            this.$root.getSocket().emit("importMonitors", monitors, dryRun, (res) => {
                this.processing = false;
                this.rows = res.rows ?? [];
                this.messageOk = res.ok;
                if (dryRun && res.ok) {
                    this.checkedText = text;
                    this.checkedCount = res.count;
                    this.message = this.$t("importMonitorsChecked", res.count);
                } else {
                    this.checkedText = null;
                    this.checkedCount = 0;
                    this.message = res.msg;
                }
                if (!dryRun && res.ok) {
                    this.$root.toastSuccess(res.msg);
                }
            });
        },
    },
};
</script>

<style lang="scss" scoped>
.example {
    font-size: 12px;
    padding: 8px;
    margin: 8px 0;
    border-radius: 6px;
    background-color: rgba(128, 128, 128, 0.1);
    white-space: pre;
}

.import-errors {
    font-size: 14px;
    color: var(--bs-danger);
}

.import-rows {
    font-size: 14px;
}
</style>
