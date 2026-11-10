<template>
    <div
        ref="modal"
        class="modal fade"
        tabindex="-1"
        aria-labelledby="shortcuts-help-title"
        data-testid="shortcuts-help"
    >
        <div class="modal-dialog">
            <div class="modal-content">
                <div class="modal-header">
                    <h5 id="shortcuts-help-title" class="modal-title">{{ $t("Keyboard shortcuts") }}</h5>
                    <button type="button" class="btn-close" data-bs-dismiss="modal" :aria-label="$t('Close')" />
                </div>
                <div class="modal-body">
                    <table class="table table-borderless mb-0">
                        <tbody>
                            <tr v-for="shortcut in shortcuts" :key="shortcut.action">
                                <td class="keys">
                                    <template v-for="(key, i) in shortcut.keys" :key="i">
                                        <span v-if="i > 0" class="then">
                                            {{ shortcut.keys[0] === "Ctrl" ? "+" : "" }}
                                        </span>
                                        <kbd>{{ key === "Ctrl" && isMac ? "⌘" : key }}</kbd>
                                    </template>
                                </td>
                                <td>{{ $t(shortcut.label) }}</td>
                            </tr>
                        </tbody>
                    </table>
                </div>
            </div>
        </div>
    </div>
</template>

<script>
import { Modal } from "bootstrap";
import { SHORTCUTS } from "../shortcuts";

export default {
    data() {
        return {
            modal: null,
            isMac: /Mac|iPhone|iPad/.test(navigator.platform),
        };
    },
    computed: {
        shortcuts() {
            const editOnly = ["newMonitor", "edit"];
            return SHORTCUTS.filter((s) => this.$root.canEdit || !editOnly.includes(s.action));
        },
    },
    mounted() {
        this.modal = new Modal(this.$refs.modal);
        this.$root.emitter.on("showShortcutsHelp", this.show);
    },
    beforeUnmount() {
        this.$root.emitter.off("showShortcutsHelp", this.show);
        this.modal?.hide();
        this.modal?.dispose();
    },
    methods: {
        /**
         * Show the list
         * @returns {void}
         */
        show() {
            this.modal.show();
        },
    },
};
</script>

<style lang="scss" scoped>
.keys {
    white-space: nowrap;
    width: 1%;
    padding-right: 24px;
}

.then {
    margin: 0 4px;
}

kbd {
    min-width: 1.8em;
    display: inline-block;
    text-align: center;
}
</style>
