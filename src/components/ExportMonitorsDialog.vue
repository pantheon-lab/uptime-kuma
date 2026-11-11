<template>
    <div
        ref="modal"
        class="modal fade"
        tabindex="-1"
        aria-labelledby="export-monitors-title"
        data-testid="export-dialog"
    >
        <div class="modal-dialog">
            <form class="modal-content" @submit.prevent="download">
                <div class="modal-header">
                    <h5 id="export-monitors-title" class="modal-title">
                        {{ $t("exportMonitorsTitle", ids.length) }}
                    </h5>
                    <button type="button" class="btn-close" data-bs-dismiss="modal" :aria-label="$t('Close')" />
                </div>
                <div class="modal-body">
                    <p class="mt-0">{{ $t("exportMonitorsHelp") }}</p>

                    <div v-if="hasGroup" class="form-check mb-2">
                        <input
                            id="export-children"
                            v-model="children"
                            class="form-check-input"
                            type="checkbox"
                            data-testid="export-children"
                        />
                        <label class="form-check-label" for="export-children">
                            {{ $t("exportMonitorsChildren") }}
                        </label>
                    </div>
                    <div class="form-check mb-3">
                        <input
                            id="export-groups"
                            v-model="groups"
                            class="form-check-input"
                            type="checkbox"
                            data-testid="export-groups"
                        />
                        <label class="form-check-label" for="export-groups">
                            {{ $t("exportMonitorsGroups") }}
                        </label>
                        <div class="form-text">
                            {{ groups ? $t("exportMonitorsGroupsHelp") : $t("exportMonitorsNoGroupsHelp") }}
                        </div>
                    </div>

                    <label for="export-format" class="form-label">{{ $t("exportMonitorsFormat") }}</label>
                    <select id="export-format" v-model="format" class="form-select mb-3" data-testid="export-format">
                        <option value="yaml">YAML</option>
                        <option value="json">JSON</option>
                    </select>

                    <div class="alert alert-warning py-2 mb-0" role="note">
                        {{ $t("exportMonitorsSecrets") }}
                    </div>
                    <div v-if="error" class="alert alert-danger py-2 mt-3 mb-0" role="alert" data-testid="export-error">
                        {{ error }}
                    </div>
                </div>
                <div class="modal-footer">
                    <button type="button" class="btn btn-outline-normal" data-bs-dismiss="modal">
                        {{ $t("Close") }}
                    </button>
                    <button type="submit" class="btn btn-primary" :disabled="processing" data-testid="export-download">
                        <font-awesome-icon icon="download" class="me-1" />
                        {{ $t("exportMonitorsDownload") }}
                    </button>
                </div>
            </form>
        </div>
    </div>
</template>

<script>
import { Modal } from "bootstrap";
import { dump } from "js-yaml";
import { exportFileName, monitorsToFile } from "../monitor-export";

/**
 * Saves the selected monitors to a file that Import reads back, in the REST API's format with
 * their credentials. Groups can come along, so that the monitors keep them on another server.
 */
export default {
    emits: ["exported"],
    data() {
        return {
            modal: null,
            ids: [],
            groups: true,
            children: true,
            format: "yaml",
            processing: false,
            error: "",
        };
    },
    computed: {
        hasGroup() {
            return this.ids.some((id) => this.$root.monitorList[id]?.type === "group");
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
         * Show the dialog for these monitors
         * @param {Array<string|number>} ids The selected monitors
         * @returns {void}
         */
        show(ids) {
            this.ids = ids.map(Number);
            this.error = "";
            this.processing = false;
            this.modal.show();
        },

        /**
         * Fetch the monitors from the server and save them as a file
         * @returns {void}
         */
        download() {
            this.error = "";
            this.processing = true;
            const options = { groups: this.groups, children: this.hasGroup && this.children };
            const format = this.format;
            this.$root.getSocket().emit("exportMonitors", this.ids, options, (res) => {
                this.processing = false;
                if (!res.ok) {
                    this.error = res.msg;
                    return;
                }
                const text = monitorsToFile(res.monitors, format, (v) => dump(v, { noRefs: true, lineWidth: -1 }));
                const type = format === "json" ? "application/json" : "application/yaml";
                const url = URL.createObjectURL(new Blob([text], { type }));
                const link = document.createElement("a");
                link.href = url;
                link.download = exportFileName(format, new Date());
                document.body.appendChild(link);
                link.click();
                link.remove();
                setTimeout(() => URL.revokeObjectURL(url), 0);
                this.$root.toastSuccess(this.$t("exportMonitorsDone", res.count));
                this.modal.hide();
                this.$emit("exported");
            });
        },
    },
};
</script>
