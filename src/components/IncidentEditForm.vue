<template>
    <div
        class="shadow-box alert mb-4 p-4 incident"
        role="alert"
        :class="'bg-' + modelValue.style"
        data-testid="incident-edit"
    >
        <strong>{{ $t("Title") }}:</strong>
        <Editable
            :model-value="modelValue.title"
            tag="h4"
            :contenteditable="true"
            :noNL="true"
            class="alert-heading"
            data-testid="incident-title"
            @update:model-value="updateField('title', $event)"
        />

        <strong>{{ $t("Content") }}:</strong>
        <Editable
            :model-value="modelValue.content"
            tag="div"
            :contenteditable="true"
            class="content"
            data-testid="incident-content-editable"
            @update:model-value="updateField('content', $event)"
        />
        <div class="form-text">
            {{ $t("markdownSupported") }}
        </div>

        <div v-if="subscriberCount > 0" class="form-check mt-3">
            <input
                id="incident-notify"
                :checked="modelValue.notify"
                type="checkbox"
                class="form-check-input"
                data-testid="incident-notify"
                @change="updateField('notify', $event.target.checked)"
            />
            <label for="incident-notify" class="form-check-label">
                {{ $t("incidentNotifySubscribers", subscriberCount) }}
            </label>
        </div>

        <div class="mt-3 d-flex flex-wrap gap-2">
            <button class="btn btn-light" data-testid="post-incident-button" @click="$emit('post')">
                <font-awesome-icon icon="bullhorn" />
                {{ $t("Post") }}
            </button>

            <button class="btn btn-light" @click="$emit('cancel')">
                <font-awesome-icon icon="times" />
                {{ $t("Cancel") }}
            </button>

            <div class="dropdown d-inline-block">
                <button
                    class="btn btn-secondary dropdown-toggle"
                    type="button"
                    data-bs-toggle="dropdown"
                    aria-expanded="false"
                    data-testid="incident-templates"
                >
                    <font-awesome-icon icon="file-alt" />
                    {{ $t("incidentTemplates") }}
                </button>
                <ul class="dropdown-menu template-menu" data-testid="incident-template-menu">
                    <template v-if="templates.length > 0">
                        <li>
                            <h6 class="dropdown-header">{{ $t("incidentTemplatesSaved") }}</h6>
                        </li>
                        <li v-for="t in templates" :key="t.id" class="d-flex align-items-center">
                            <a
                                class="dropdown-item flex-grow-1"
                                href="#"
                                data-testid="incident-template"
                                @click.prevent="apply(t)"
                            >
                                {{ t.title }}
                            </a>
                            <button
                                type="button"
                                class="btn btn-sm btn-link text-danger me-2"
                                :aria-label="$t('incidentTemplateDelete', [t.title])"
                                data-testid="incident-template-delete"
                                @click.stop="remove(t)"
                            >
                                <font-awesome-icon icon="times" />
                            </button>
                        </li>
                        <li><hr class="dropdown-divider" /></li>
                    </template>
                    <li>
                        <h6 class="dropdown-header">{{ $t("incidentTemplatesStarters") }}</h6>
                    </li>
                    <li v-for="t in starters" :key="t.key">
                        <a
                            class="dropdown-item"
                            href="#"
                            data-testid="incident-starter"
                            @click.prevent="apply(starterTemplate(t))"
                        >
                            {{ $t(`incidentStarter-${t.key}`) }}
                        </a>
                    </li>
                    <li><hr class="dropdown-divider" /></li>
                    <li>
                        <a
                            class="dropdown-item"
                            href="#"
                            data-testid="incident-template-save"
                            @click.prevent="saveTemplate"
                        >
                            <font-awesome-icon icon="save" class="me-1" />
                            {{ $t("incidentTemplateSave") }}
                        </a>
                    </li>
                </ul>
            </div>

            <div class="dropdown d-inline-block">
                <button
                    id="dropdownMenuButton1"
                    class="btn btn-secondary dropdown-toggle"
                    type="button"
                    data-bs-toggle="dropdown"
                    aria-expanded="false"
                >
                    {{ $t("Style") }}: {{ $t(modelValue.style) }}
                </button>
                <ul class="dropdown-menu" aria-labelledby="dropdownMenuButton1">
                    <li>
                        <a class="dropdown-item" href="#" @click.prevent="updateField('style', 'info')">
                            {{ $t("info") }}
                        </a>
                    </li>
                    <li>
                        <a class="dropdown-item" href="#" @click.prevent="updateField('style', 'warning')">
                            {{ $t("warning") }}
                        </a>
                    </li>
                    <li>
                        <a class="dropdown-item" href="#" @click.prevent="updateField('style', 'danger')">
                            {{ $t("danger") }}
                        </a>
                    </li>
                    <li>
                        <a class="dropdown-item" href="#" @click.prevent="updateField('style', 'primary')">
                            {{ $t("primary") }}
                        </a>
                    </li>
                    <li>
                        <a class="dropdown-item" href="#" @click.prevent="updateField('style', 'light')">
                            {{ $t("light") }}
                        </a>
                    </li>
                    <li>
                        <a class="dropdown-item" href="#" @click.prevent="updateField('style', 'dark')">
                            {{ $t("dark") }}
                        </a>
                    </li>
                </ul>
            </div>
        </div>
    </div>
</template>

<script>
export default {
    name: "IncidentEditForm",
    props: {
        modelValue: {
            type: Object,
            required: true,
        },
        // Confirmed subscribers, who can be sent the incident
        subscriberCount: {
            type: Number,
            default: 0,
        },
    },
    emits: ["update:modelValue", "post", "cancel"],
    data() {
        return {
            templates: [],
            // Ready to use where there are no templates yet
            starters: [
                { key: "investigating", style: "warning" },
                { key: "identified", style: "danger" },
                { key: "monitoring", style: "info" },
                { key: "maintenance", style: "primary" },
            ],
        };
    },
    mounted() {
        this.$root.getSocket().emit("getIncidentTemplates", (res) => {
            if (res.ok) {
                this.templates = res.templates;
            }
        });
    },
    methods: {
        /**
         * A starter in the UI's language
         * @param {object} starter Its key and style
         * @returns {object} Title, content and style
         */
        starterTemplate(starter) {
            return {
                title: this.$t(`incidentStarter-${starter.key}`),
                content: this.$t(`incidentStarter-${starter.key}-content`),
                style: starter.style,
            };
        },

        /**
         * Fill the form from a template, keeping the rest
         * @param {object} t The template
         * @returns {void}
         */
        apply(t) {
            this.$emit("update:modelValue", {
                ...this.modelValue,
                title: t.title,
                content: t.content,
                style: t.style,
            });
        },

        /**
         * Keep the form as a template, which replaces one with its title
         * @returns {void}
         */
        saveTemplate() {
            const { title, content, style } = this.modelValue;
            this.$root.getSocket().emit("saveIncidentTemplate", { title, content, style }, (res) => {
                this.$root.toastRes(res);
                if (res.ok) {
                    this.templates = [...this.templates.filter((t) => t.id !== res.template.id), res.template].sort(
                        (a, b) => a.title.localeCompare(b.title)
                    );
                }
            });
        },

        /**
         * Delete a template
         * @param {object} t The template
         * @returns {void}
         */
        remove(t) {
            this.$root.getSocket().emit("deleteIncidentTemplate", t.id, (res) => {
                this.$root.toastRes(res);
                if (res.ok) {
                    this.templates = this.templates.filter((x) => x.id !== t.id);
                }
            });
        },

        updateField(field, value) {
            this.$emit("update:modelValue", {
                ...this.modelValue,
                [field]: value,
            });
        },
    },
};
</script>

<style lang="scss" scoped>
.template-menu {
    min-width: 16rem;
    max-height: 60vh;
    overflow-y: auto;
}

.incident {
    .content {
        &[contenteditable="true"] {
            min-height: 60px;
        }
    }
}
</style>
