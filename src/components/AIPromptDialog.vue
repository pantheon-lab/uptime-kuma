<template>
    <div ref="modal" class="modal fade" tabindex="-1" aria-labelledby="ai-prompt-title" data-testid="ai-prompt-dialog">
        <div class="modal-dialog modal-lg modal-dialog-scrollable">
            <div class="modal-content">
                <div class="modal-header">
                    <h5 id="ai-prompt-title" class="modal-title">
                        <font-awesome-icon icon="robot" class="me-2" />
                        {{ $t("aiPromptTitle") }}
                    </h5>
                    <button type="button" class="btn-close" data-bs-dismiss="modal" :aria-label="$t('Close')" />
                </div>
                <div class="modal-body">
                    <p class="mb-2">{{ $t("aiPromptHelp") }}</p>
                    <p class="form-text mb-3">{{ $t("aiPromptPrivacy") }}</p>
                    <div v-if="loading" class="text-center my-4" aria-busy="true">
                        <div class="spinner-border" role="status"></div>
                    </div>
                    <textarea
                        v-else
                        ref="text"
                        class="form-control prompt"
                        rows="18"
                        readonly
                        data-testid="ai-prompt-text"
                        :aria-label="$t('aiPromptTitle')"
                        :value="prompt"
                    ></textarea>
                </div>
                <div class="modal-footer">
                    <span v-if="copied" class="text-success me-auto" role="status">{{ $t("aiPromptCopied") }}</span>
                    <button
                        type="button"
                        class="btn btn-outline-normal"
                        :disabled="loading"
                        data-testid="ai-prompt-download"
                        @click="download"
                    >
                        <font-awesome-icon icon="download" class="me-1" />
                        {{ $t("aiPromptDownload") }}
                    </button>
                    <button
                        type="button"
                        class="btn btn-primary"
                        :disabled="loading"
                        data-testid="ai-prompt-copy"
                        @click="copy"
                    >
                        <font-awesome-icon icon="copy" class="me-1" />
                        {{ $t("aiPromptCopy") }}
                    </button>
                </div>
            </div>
        </div>
    </div>
</template>

<script>
import { Modal } from "bootstrap";
import { buildIncidentPrompt } from "../ai-prompt";
import { UP } from "../util";

// Monitors in one prompt at most, so that it stays readable
const MAX_MONITORS = 20;
// Status changes the prompt has for each monitor
const EVENTS_PER_MONITOR = 10;
// How long the dialog waits for a monitor's status changes, in milliseconds
const EVENTS_TIMEOUT = 10000;

export default {
    data() {
        return {
            modal: null,
            loading: false,
            prompt: "",
            copied: false,
            fileName: "monitor-failure.md",
        };
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
         * Build the prompt for these monitors and show it
         * @param {number[]} monitorIDs Failing monitors
         * @returns {Promise<void>}
         */
        async show(monitorIDs) {
            const ids = monitorIDs.slice(0, MAX_MONITORS);
            this.copied = false;
            this.loading = true;
            this.prompt = "";
            this.modal.show();
            try {
                const events = await this.loadEvents(ids);
                const inputs = ids
                    .filter((id) => this.$root.monitorList[id])
                    .map((id) =>
                        this.promptInput(
                            id,
                            events.filter((e) => e.monitorID === id)
                        )
                    );
                this.prompt = buildIncidentPrompt(inputs, {
                    now: new Date(),
                    baseURL: this.$root.baseURL,
                });
                const first = this.$root.monitorList[ids[0]]?.name ?? "monitor";
                const slug =
                    first
                        .toLowerCase()
                        .replace(/[^a-z0-9]+/g, "-")
                        .replace(/^-|-$/g, "") || "monitor";
                this.fileName = ids.length > 1 ? "monitor-failures.md" : `${slug}-failure.md`;
            } finally {
                this.loading = false;
            }
        },

        /**
         * The monitors' recent status changes, asked for each monitor so that one that flaps
         * can't push out the others'. A monitor whose answer doesn't come in time has none.
         * @param {number[]} ids Monitors
         * @returns {Promise<object[]>} Events
         */
        async loadEvents(ids) {
            const one = (id) =>
                new Promise((resolve) => {
                    const timer = setTimeout(() => resolve([]), EVENTS_TIMEOUT);
                    this.$root
                        .getSocket()
                        .emit("monitorImportantHeartbeatListPaged", id, 0, EVENTS_PER_MONITOR, (res) => {
                            clearTimeout(timer);
                            resolve(res?.ok ? res.data : []);
                        });
                });
            return (await Promise.all(ids.map(one))).flat();
        },

        /**
         * What the prompt says about a monitor, from what the page has
         * @param {number} id Monitor
         * @param {object[]} events Its status changes
         * @returns {object} Prompt input
         */
        promptInput(id, events) {
            const root = this.$root;
            const monitor = root.monitorList[id];
            const failingChildren = [];
            if (monitor.type === "group") {
                const stack = [...(monitor.childrenIDs ?? [])];
                const seen = new Set();
                while (stack.length > 0) {
                    const childID = stack.pop();
                    const child = root.monitorList[childID];
                    if (!child || seen.has(childID)) {
                        continue;
                    }
                    seen.add(childID);
                    stack.push(...(child.childrenIDs ?? []));
                    const beat = root.lastHeartbeatList[childID];
                    if (child.active && child.type !== "group" && beat && beat.status !== UP) {
                        failingChildren.push({
                            name: child.pathName || child.name,
                            status: beat.status,
                            msg: beat.msg,
                        });
                    }
                }
            }
            return {
                monitor,
                beats: root.heartbeatList[id] ?? [],
                events,
                uptime24: root.uptimeList[`${id}_24`],
                uptime30d: root.uptimeList[`${id}_720`],
                certificate: root.tlsInfoList[id]?.certInfo ?? null,
                failingChildren,
            };
        },

        /**
         * Copy the prompt
         * @returns {Promise<void>}
         */
        async copy() {
            try {
                await navigator.clipboard.writeText(this.prompt);
            } catch {
                // Without the clipboard API (plain http), copy the selection
                this.$refs.text.select();
                document.execCommand("copy");
            }
            this.copied = true;
        },

        /**
         * Save the prompt as a Markdown file
         * @returns {void}
         */
        download() {
            const url = URL.createObjectURL(new Blob([this.prompt], { type: "text/markdown" }));
            const a = document.createElement("a");
            a.href = url;
            a.download = this.fileName;
            a.click();
            setTimeout(() => URL.revokeObjectURL(url), 1000);
        },
    },
};
</script>

<style lang="scss" scoped>
.prompt {
    font-family: SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace;
    font-size: 13px;
    white-space: pre-wrap;
}
</style>
