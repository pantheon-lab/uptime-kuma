<template>
    <nav
        v-if="sections.length > 1"
        class="form-section-nav"
        :aria-label="$t('formSections')"
        data-testid="form-section-nav"
    >
        <button
            v-for="(section, i) in sections"
            :key="section.id"
            type="button"
            class="btn btn-sm section-link"
            :class="{ active: i === active }"
            :aria-current="i === active ? 'location' : null"
            @click="jump(i)"
        >
            {{ section.label }}
        </button>
    </nav>
</template>

<script>
/**
 * Links to a form's sections, from its h2 headings, which stay in view as the form scrolls. The
 * links follow the headings shown, so they change as fields appear and disappear.
 */
export default {
    props: {
        /** The element whose h2 headings are the sections */
        container: {
            type: Object,
            default: null,
        },
    },
    data() {
        return {
            sections: [],
            active: 0,
        };
    },
    watch: {
        container: {
            handler() {
                this.observe();
            },
            immediate: true,
        },
    },
    beforeUnmount() {
        this.observer?.disconnect();
        window.removeEventListener("scroll", this.onScroll);
        cancelAnimationFrame(this.frame);
    },
    methods: {
        /**
         * Follow the container's headings as they change
         * @returns {void}
         */
        observe() {
            this.observer?.disconnect();
            window.removeEventListener("scroll", this.onScroll);
            if (!this.container) {
                return;
            }
            this.observer = new MutationObserver(() => this.schedule());
            this.observer.observe(this.container, { childList: true, subtree: true });
            window.addEventListener("scroll", this.onScroll, { passive: true });
            this.schedule();
        },

        /**
         * Read the headings once per frame, however many changes there were
         * @returns {void}
         */
        schedule() {
            cancelAnimationFrame(this.frame);
            this.frame = requestAnimationFrame(() => {
                this.headings = [...this.container.querySelectorAll("h2")].filter(
                    (h) => h.offsetParent !== null && !h.closest(".modal")
                );
                const sections = this.headings.map((h, i) => {
                    if (!h.id) {
                        h.id = `form-section-${i}-${Math.random().toString(36).slice(2, 8)}`;
                    }
                    return { id: h.id, label: h.textContent.replace(/\s+/g, " ").trim() };
                });
                if (JSON.stringify(sections) !== JSON.stringify(this.sections)) {
                    this.sections = sections;
                }
                this.onScroll();
            });
        },

        /**
         * Mark the section being read: the last heading above the middle of the top half
         * @returns {void}
         */
        onScroll() {
            const line = window.innerHeight * 0.25;
            let active = 0;
            (this.headings ?? []).forEach((h, i) => {
                if (h.getBoundingClientRect().top <= line) {
                    active = i;
                }
            });
            this.active = active;
        },

        /**
         * Scroll to a section
         * @param {number} i Its index
         * @returns {void}
         */
        jump(i) {
            const heading = this.headings?.[i];
            if (heading) {
                heading.scrollIntoView({ behavior: "smooth", block: "start" });
                this.active = i;
            }
        },
    },
};
</script>

<style lang="scss" scoped>
@import "../assets/vars";

.form-section-nav {
    position: sticky;
    top: 0;
    z-index: 5;
    display: flex;
    gap: 6px;
    overflow-x: auto;
    margin: 0 0 12px;
    padding: 8px 0;
    background-color: #fff;
    border-bottom: 1px solid rgba(0, 0, 0, 0.08);

    .dark & {
        background-color: $dark-bg;
        border-bottom-color: rgba(255, 255, 255, 0.08);
    }
}

.section-link {
    flex-shrink: 0;
    border-radius: 50rem;
    color: inherit;
    opacity: 0.75;

    &:hover {
        opacity: 1;
    }

    &.active {
        opacity: 1;
        background-color: rgba($primary, 0.15);
        font-weight: 600;
    }
}
</style>
