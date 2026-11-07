<template>
    <div>
        <p class="mt-4 text-secondary small">
            <template v-if="githubLogin">{{ $t("accountsGitHubHint") }}</template>
            {{ $t("accountsLastAdminHint") }}
        </p>

        <div v-if="loaded && accounts.length === 0" class="d-flex justify-content-center my-3">
            {{ $t("No accounts") }}
        </div>

        <div
            v-for="account in accounts"
            :key="account.id"
            class="item"
            :class="account.role"
            :data-testid="`account-${account.username}`"
        >
            <div class="left-part">
                <div class="circle"></div>
                <div class="info">
                    <div class="title">
                        {{ account.username }}
                        <span v-if="account.you" class="badge bg-secondary ms-1">{{ $t("You") }}</span>
                    </div>
                    <div class="status">
                        {{ account.role === "admin" ? $t("accountRoleAdmin") : $t("accountRoleViewer") }}
                        ·
                        {{ account.method === "github" ? $t("accountMethodGitHub") : $t("accountMethodPassword") }}
                        <template v-if="account.twoFactorEnabled">· {{ $t("Two Factor Authentication") }}</template>
                    </div>
                    <div class="date">{{ $t("createdAt", { date: account.createdAt }) }}</div>
                    <div class="date">{{ $t("accountLastLogin") }}: {{ account.lastLoginAt || $t("Never") }}</div>
                    <div v-if="account.method === 'github'" class="date">
                        {{ $t("accountTeamsChecked") }}: {{ account.githubCheckedAt || $t("Never") }}
                    </div>
                    <div class="date" data-testid="sessions">
                        {{ $t("accountSessions", { count: account.sessions.length }) }}
                    </div>
                    <ul v-if="account.sessions.length" class="sessions">
                        <li v-for="(session, i) in account.sessions" :key="i">
                            {{ $t("accountSessionSince", { date: session.createdAt }) }},
                            {{ $t("accountSessionUntil", { date: session.expiresAt }) }}
                            <span v-if="session.current" class="badge bg-primary ms-1">{{ $t("This session") }}</span>
                        </li>
                    </ul>
                </div>
            </div>

            <div class="buttons">
                <select
                    v-if="account.method === 'password'"
                    class="form-select role-select"
                    :value="account.role"
                    :disabled="account.you"
                    :title="account.you ? $t('accountOwnHint') : ''"
                    data-testid="role"
                    @change="setRole(account, $event.target.value)"
                >
                    <option value="admin">{{ $t("accountRoleAdmin") }}</option>
                    <option value="viewer">{{ $t("accountRoleViewer") }}</option>
                </select>
                <button
                    class="btn btn-outline-danger"
                    :disabled="account.sessions.length === 0 || (account.you && account.sessions.length === 1)"
                    data-testid="end-sessions"
                    @click="confirm('end', account)"
                >
                    {{ $t("End sessions") }}
                </button>
                <button
                    class="btn btn-danger"
                    :disabled="account.you"
                    :title="account.you ? $t('accountOwnHint') : ''"
                    data-testid="remove"
                    @click="confirm('remove', account)"
                >
                    <font-awesome-icon icon="trash" />
                    {{ $t("Remove") }}
                </button>
            </div>
        </div>

        <Confirm ref="confirmDialog" btn-style="btn-danger" :yes-text="$t('Yes')" :no-text="$t('No')" @yes="act">
            <template v-if="selected && action === 'remove'">
                {{ $t("removeAccountMsg", { username: selected.username }) }}
                <template v-if="selected.method === 'github'">
                    {{ $t("removeGitHubAccountMsg") }}
                </template>
            </template>
            <template v-else-if="selected">
                {{ $t(selected.you ? "endOwnSessionsMsg" : "endSessionsMsg", { username: selected.username }) }}
            </template>
        </Confirm>
    </div>
</template>

<script>
import Confirm from "../Confirm.vue";

export default {
    components: {
        Confirm,
    },
    data() {
        return {
            accounts: [],
            githubLogin: false,
            loaded: false,
            selected: null,
            action: null,
        };
    },

    mounted() {
        this.load();
    },

    methods: {
        /**
         * Load every account
         * @returns {void}
         */
        load() {
            this.$root.getSocket().emit("getAccountList", (res) => {
                if (res.ok) {
                    this.accounts = res.accounts;
                    this.githubLogin = res.githubLogin;
                    this.loaded = true;
                } else {
                    this.$root.toastError(res.msg);
                }
            });
        },

        /**
         * Ask before ending an account's sessions or removing it
         * @param {string} action "end" or "remove"
         * @param {object} account The account
         * @returns {void}
         */
        confirm(action, account) {
            this.action = action;
            this.selected = account;
            this.$refs.confirmDialog.show();
        },

        /**
         * Do what was confirmed
         * @returns {void}
         */
        act() {
            const event = this.action === "remove" ? "deleteAccount" : "endAccountSessions";
            this.$root.getSocket().emit(event, this.selected.id, (res) => {
                this.$root.toastRes(res);
                this.load();
            });
        },

        /**
         * Change a password account's role
         * @param {object} account The account
         * @param {string} role "admin" or "viewer"
         * @returns {void}
         */
        setRole(account, role) {
            this.$root.getSocket().emit("setAccountRole", account.id, role, (res) => {
                this.$root.toastRes(res);
                this.load();
            });
        },
    },
};
</script>

<style lang="scss" scoped>
@import "../../assets/vars.scss";

.item {
    display: flex;
    align-items: center;
    gap: 10px;
    border-radius: 10px;
    justify-content: space-between;
    padding: 10px;
    margin-bottom: 5px;

    &:hover {
        background-color: $highlight-white;
    }

    &.admin .circle {
        background-color: $primary;
    }

    &.viewer .circle {
        background-color: $dark-font-color;
    }

    .left-part {
        display: flex;
        gap: 12px;
        align-items: center;

        .circle {
            flex-shrink: 0;
            width: 25px;
            height: 25px;
            border-radius: 50rem;
        }

        .title {
            font-weight: bold;
            font-size: 20px;
        }

        .status {
            font-size: 14px;
        }
    }

    .buttons {
        display: flex;
        gap: 8px;
        align-items: center;
        flex-wrap: wrap;
        justify-content: flex-end;

        .role-select {
            width: auto;
        }
    }
}

.mobile .item {
    flex-direction: column;
    align-items: flex-start;
}

.sessions {
    margin: 5px 0 0;
    padding-left: 20px;
    font-size: 13px;
}

.date {
    margin-top: 5px;
    display: block;
    font-size: 14px;
    background-color: rgba(255, 255, 255, 0.5);
    border-radius: 20px;
    padding: 0 10px;
    width: fit-content;

    .dark & {
        color: white;
        background-color: rgba(255, 255, 255, 0.1);
    }
}

.dark .item:hover {
    background-color: $dark-bg2;
}
</style>
