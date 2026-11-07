<template>
    <div class="my-4">
        <p class="mx-0 mx-lg-4">{{ $t("holidaysSettingsDescription") }}</p>

        <div class="row mx-0 mx-lg-3 mb-3">
            <div class="col-12 col-md-8 mb-2">
                <label for="holiday-country" class="form-label">{{ $t("Country") }}</label>
                <select id="holiday-country" v-model="country" class="form-select">
                    <option v-for="item in countries" :key="item.code" :value="item.code">
                        {{ item.name }} ({{ item.code }})
                    </option>
                </select>
            </div>
            <div class="col-12 col-md-4 mb-2">
                <label for="holiday-year" class="form-label">{{ $t("Year") }}</label>
                <input
                    id="holiday-year"
                    v-model.number="year"
                    type="number"
                    class="form-control"
                    min="1970"
                    max="9999"
                />
            </div>
        </div>

        <form class="row mx-0 mx-lg-3 mb-3 align-items-end" @submit.prevent="add">
            <div class="col-12 col-md-4 mb-2">
                <label for="holiday-date" class="form-label">{{ $t("Date") }}</label>
                <input id="holiday-date" v-model="newDate" type="date" class="form-control" required />
            </div>
            <div class="col-12 col-md-5 mb-2">
                <label for="holiday-name" class="form-label">{{ $t("Name") }}</label>
                <input id="holiday-name" v-model="newName" type="text" class="form-control" maxlength="200" required />
            </div>
            <div class="col-12 col-md-3 mb-2">
                <button type="submit" class="btn btn-primary w-100" :disabled="processing">
                    <font-awesome-icon icon="plus" />
                    {{ $t("Add Holiday") }}
                </button>
            </div>
        </form>

        <div class="mx-0 mx-lg-4">
            <table class="table table-borderless align-middle holiday-table">
                <thead>
                    <tr>
                        <th>{{ $t("Date") }}</th>
                        <th>{{ $t("Name") }}</th>
                        <th></th>
                    </tr>
                </thead>
                <tbody>
                    <tr v-if="holidays.length === 0">
                        <td colspan="3" class="text-secondary">{{ $t("No holidays for this year") }}</td>
                    </tr>
                    <tr v-for="holiday in holidays" :key="holiday.date" :class="{ removed: holiday.removed }">
                        <td class="text-nowrap">{{ holiday.date }}</td>
                        <td>
                            {{ holiday.name }}
                            <span v-if="holiday.source === 'custom'" class="badge bg-primary ms-1">
                                {{ $t("Custom") }}
                            </span>
                            <span v-if="holiday.removed" class="badge bg-secondary ms-1">{{ $t("Removed") }}</span>
                        </td>
                        <td class="text-end">
                            <button
                                v-if="holiday.removed"
                                type="button"
                                class="btn btn-outline-primary btn-sm"
                                :disabled="processing"
                                @click="restore(holiday)"
                            >
                                {{ $t("Restore") }}
                            </button>
                            <button
                                v-else
                                type="button"
                                class="btn btn-outline-danger btn-sm"
                                :disabled="processing"
                                :aria-label="$t('Delete')"
                                @click="remove(holiday)"
                            >
                                <font-awesome-icon icon="trash" />
                            </button>
                        </td>
                    </tr>
                </tbody>
            </table>
            <p class="form-text">{{ $t("holidayDataSource", { source }) }}</p>
        </div>
    </div>
</template>

<script>
const DEFAULT_COUNTRY = "HK";

export default {
    data() {
        return {
            countries: [],
            source: "",
            country: localStorage.holidayCountry || DEFAULT_COUNTRY,
            year: new Date().getFullYear(),
            holidays: [],
            newDate: "",
            newName: "",
            processing: false,
        };
    },
    watch: {
        country(value) {
            localStorage.holidayCountry = value;
            this.load();
        },
        year() {
            this.load();
        },
    },
    mounted() {
        this.$root.getSocket().emit("getHolidayCountries", (res) => {
            if (!res.ok) {
                this.$root.toastRes(res);
                return;
            }
            this.countries = res.countries;
            this.source = res.source;
        });
        this.load();
    },
    methods: {
        /**
         * Load the chosen country's holidays for the chosen year
         * @returns {void}
         */
        load() {
            if (!Number.isInteger(this.year) || this.year < 1970 || this.year > 9999) {
                this.holidays = [];
                return;
            }
            const { country, year } = this;
            this.$root.getSocket().emit("getHolidays", country, year, (res) => {
                if (country !== this.country || year !== this.year) {
                    return; // The selection changed while this was on its way.
                }
                if (res.ok) {
                    this.holidays = res.holidays;
                } else {
                    this.$root.toastRes(res);
                }
            });
        },

        /**
         * Send a change to the calendar, then reload it
         * @param {string} event Event name
         * @param {...any} args Event arguments
         * @returns {void}
         */
        change(event, ...args) {
            this.processing = true;
            this.$root.getSocket().emit(event, this.country, ...args, (res) => {
                this.processing = false;
                this.$root.toastRes(res);
                if (res.ok) {
                    this.load();
                }
            });
        },

        /**
         * Add the date in the form to the calendar
         * @returns {void}
         */
        add() {
            this.change("addCustomHoliday", this.newDate, this.newName);
            this.year = Number(this.newDate.slice(0, 4));
            this.newDate = "";
            this.newName = "";
        },

        /**
         * Remove a date. A bundled one can be restored later.
         * @param {object} holiday Holiday to remove
         * @returns {void}
         */
        remove(holiday) {
            this.change("removeHoliday", holiday.date);
        },

        /**
         * Bring back a removed bundled date
         * @param {object} holiday Holiday to restore
         * @returns {void}
         */
        restore(holiday) {
            this.change("restoreHoliday", holiday.date);
        },
    },
};
</script>

<style lang="scss" scoped>
.holiday-table .removed td:not(:last-child) {
    opacity: 0.5;
    text-decoration: line-through;
}
</style>
