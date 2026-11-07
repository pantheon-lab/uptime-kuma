<template>
    <div
        ref="modal"
        class="modal fade"
        tabindex="-1"
        data-command-palette
        data-testid="command-palette"
        :aria-label="$t('Command palette')"
    >
        <div class="modal-dialog modal-dialog-scrollable palette-dialog">
            <div class="modal-content">
                <div class="modal-header p-2">
                    <input
                        ref="input"
                        v-model="query"
                        class="form-control palette-input"
                        type="text"
                        role="combobox"
                        aria-autocomplete="list"
                        aria-controls="command-palette-results"
                        :aria-activedescendant="results.length ? `command-${selected}` : null"
                        aria-expanded="true"
                        autocomplete="off"
                        spellcheck="false"
                        data-testid="command-palette-input"
                        :placeholder="$t('commandPalettePlaceholder')"
                        @keydown.down.prevent="move(1)"
                        @keydown.up.prevent="move(-1)"
                        @keydown.enter.prevent="choose(results[selected])"
                    />
                </div>
                <div class="modal-body p-2">
                    <ul v-if="results.length" id="command-palette-results" ref="list" class="results" role="listbox">
                        <li
                            v-for="(command, i) in results"
                            :id="`command-${i}`"
                            :key="command.id"
                            role="option"
                            class="result"
                            :class="{ selected: i === selected }"
                            :aria-selected="i === selected"
                            @mousemove="selected = i"
                            @click="choose(command)"
                        >
                            <span class="label">{{ command.label }}</span>
                            <span class="group">{{ command.group }}</span>
                        </li>
                    </ul>
                    <p v-else class="text-secondary m-2">{{ $t("commandPaletteNoMatch") }}</p>
                </div>
            </div>
        </div>
    </div>
</template>

<script>
import { Modal } from "bootstrap";
import { searchCommands } from "../command-search";
import { SETTINGS_PAGES } from "../page-title";

export default {
    data() {
        return {
            modal: null,
            query: "",
            selected: 0,
        };
    },
    computed: {
        /**
         * Everything the palette goes to
         * @returns {object[]} Commands
         */
        commands() {
            const root = this.$root;
            const commands = [];
            const action = (id, label, to) => commands.push({ id, label, group: this.$t("commandGroupAction"), to });
            if (root.canEdit) {
                action("add-monitor", this.$t("Add New Monitor"), "/add");
                action("add-status-page", this.$t("Add New Status Page"), "/add-status-page");
                action("add-maintenance", this.$t("Schedule Maintenance"), "/add-maintenance");
            }
            action("dashboard", this.$t("Dashboard"), "/dashboard");
            action("status-pages", this.$t("Status Pages"), "/manage-status-page");
            action("maintenance", this.$t("Maintenance"), "/maintenance");

            const monitors = Object.values(root.monitorList).sort((a, b) =>
                (a.pathName || a.name).localeCompare(b.pathName || b.name)
            );
            for (const m of monitors) {
                const keywords = [m.url, m.hostname, ...(m.tags || []).map((t) => t.name)].filter(Boolean).join(" ");
                commands.push({
                    id: `monitor-${m.id}`,
                    label: m.pathName || m.name,
                    group: this.$t("commandGroupMonitor"),
                    to: `/dashboard/${m.id}`,
                    keywords,
                });
            }
            for (const page of Object.values(root.statusPageList || {})) {
                commands.push({
                    id: `status-${page.slug}`,
                    label: page.title,
                    group: this.$t("Status Page"),
                    to: `/status/${page.slug}`,
                    keywords: page.slug,
                });
            }
            for (const item of Object.values(root.maintenanceList || {})) {
                commands.push({
                    id: `maintenance-${item.id}`,
                    label: item.title,
                    group: this.$t("Maintenance"),
                    to: root.canEdit ? `/maintenance/edit/${item.id}` : "/maintenance",
                });
            }
            for (const [path, key] of Object.entries(SETTINGS_PAGES)) {
                if (root.canEdit || path === "security") {
                    commands.push({
                        id: `settings-${path}`,
                        label: this.$t(key),
                        group: this.$t("Settings"),
                        to: `/settings/${path}`,
                    });
                }
            }
            return commands;
        },

        results() {
            return searchCommands(this.commands, this.query);
        },
    },
    watch: {
        query() {
            this.selected = 0;
        },
    },
    mounted() {
        this.modal = new Modal(this.$refs.modal);
        this.$refs.modal.addEventListener("shown.bs.modal", this.onShown);
        this.$root.emitter.on("toggleCommandPalette", this.toggle);
    },
    beforeUnmount() {
        this.$root.emitter.off("toggleCommandPalette", this.toggle);
        this.modal?.hide();
        this.modal?.dispose();
    },
    methods: {
        /**
         * Open the palette, or close it when it's open
         * @returns {void}
         */
        toggle() {
            // Bootstrap's own state, which a separate flag can fall out of step with
            if (this.$refs.modal.classList.contains("show")) {
                this.modal.hide();
            } else {
                this.query = "";
                this.selected = 0;
                this.modal.show();
            }
        },

        onShown() {
            this.$refs.input.focus();
        },

        /**
         * Move the selection
         * @param {number} delta 1 for down, -1 for up
         * @returns {void}
         */
        move(delta) {
            const count = this.results.length;
            if (count === 0) {
                return;
            }
            this.selected = (this.selected + delta + count) % count;
            this.$nextTick(() => {
                document.getElementById(`command-${this.selected}`)?.scrollIntoView({ block: "nearest" });
            });
        },

        /**
         * Go where a command goes
         * @param {object} command The command
         * @returns {void}
         */
        choose(command) {
            if (!command) {
                return;
            }
            this.modal.hide();
            if (this.$route.path !== command.to) {
                this.$router.push(command.to);
            }
        },
    },
};
</script>

<style lang="scss" scoped>
@import "../assets/vars";

.palette-dialog {
    margin-top: 10vh;
    max-width: 600px;
}

.palette-input {
    border: 0;
    box-shadow: none;
    font-size: 1.1rem;
}

.results {
    list-style: none;
    margin: 0;
    padding: 0;
}

.result {
    display: flex;
    justify-content: space-between;
    gap: 12px;
    padding: 8px 12px;
    border-radius: 8px;
    cursor: pointer;

    .label {
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .group {
        flex-shrink: 0;
        font-size: 0.85em;
        opacity: 0.6;
    }

    &.selected {
        background-color: rgba($primary, 0.15);
    }
}
</style>
