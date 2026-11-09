<template>
    <div class="my-4 sso" data-testid="sso-settings">
        <div v-if="!loaded" class="spinner-border spinner-border-sm text-secondary" role="status"></div>
        <form v-else @submit.prevent="save">
            <div v-if="managedByEnv" class="alert alert-info" role="status" data-testid="sso-from-env">
                {{ $t("ssoFromEnv") }}
            </div>

            <!-- GitHub -->
            <h5 class="settings-subheading">{{ $t("ssoGitHub") }}</h5>
            <div class="form-check form-switch mb-3">
                <input
                    id="sso-github-enabled"
                    v-model="form.githubEnabled"
                    class="form-check-input"
                    type="checkbox"
                    role="switch"
                    :disabled="locked"
                    data-testid="sso-github-enabled"
                />
                <label class="form-check-label" for="sso-github-enabled">{{ $t("ssoGitHubEnabled") }}</label>
                <div class="form-text">{{ $t("ssoGitHubHelp") }}</div>
            </div>

            <fieldset v-if="form.githubEnabled" :disabled="locked" class="ms-md-4 mb-4">
                <ol class="setup-steps form-text">
                    <li>{{ $t("ssoStepRegister") }}</li>
                    <li>{{ $t("ssoStepFill") }}</li>
                    <li>{{ $t("ssoStepTry") }}</li>
                </ol>

                <div class="mb-3">
                    <label for="sso-callback" class="form-label">{{ $t("ssoCallbackURL") }}</label>
                    <CopyableInput id="sso-callback" :model-value="callback.url" data-testid="sso-callback" />
                    <div v-if="callback.guessed" class="form-text text-warning-emphasis">
                        {{ $t("ssoCallbackGuessed") }}
                    </div>
                </div>

                <div class="row g-3 mb-3">
                    <div class="col-md-6">
                        <label for="sso-client-id" class="form-label">{{ $t("ssoClientID") }}</label>
                        <input
                            id="sso-client-id"
                            v-model.trim="form.clientId"
                            type="text"
                            class="form-control"
                            autocomplete="off"
                            required
                            data-testid="sso-client-id"
                        />
                    </div>
                    <div class="col-md-6">
                        <label for="sso-client-secret" class="form-label">{{ $t("ssoClientSecret") }}</label>
                        <HiddenInput
                            id="sso-client-secret"
                            v-model="form.clientSecret"
                            :placeholder="hasClientSecret ? $t('ssoSecretKept') : ''"
                            data-testid="sso-client-secret"
                        />
                    </div>
                </div>

                <div class="mb-3">
                    <label for="sso-org" class="form-label">{{ $t("ssoOrg") }}</label>
                    <input
                        id="sso-org"
                        v-model.trim="form.org"
                        type="text"
                        class="form-control"
                        required
                        placeholder="pantheon-lab"
                        data-testid="sso-org"
                    />
                </div>

                <div class="row g-3 mb-1">
                    <div class="col-md-6">
                        <label for="sso-admin-teams" class="form-label">{{ $t("ssoAdminTeams") }}</label>
                        <input
                            id="sso-admin-teams"
                            v-model="adminTeams"
                            type="text"
                            class="form-control"
                            placeholder="sre, platform"
                            data-testid="sso-admin-teams"
                        />
                    </div>
                    <div class="col-md-6">
                        <label for="sso-viewer-teams" class="form-label">{{ $t("ssoViewerTeams") }}</label>
                        <input
                            id="sso-viewer-teams"
                            v-model="viewerTeams"
                            type="text"
                            class="form-control"
                            placeholder="support"
                            data-testid="sso-viewer-teams"
                        />
                    </div>
                </div>
                <div class="form-text mb-3">{{ $t("ssoTeamsHelp") }}</div>

                <details class="mb-3" :open="!!(form.webURL || form.apiURL)">
                    <summary>{{ $t("ssoEnterprise") }}</summary>
                    <div class="row g-3 mt-1">
                        <div class="col-md-6">
                            <label for="sso-web-url" class="form-label">{{ $t("ssoWebURL") }}</label>
                            <input
                                id="sso-web-url"
                                v-model.trim="form.webURL"
                                type="url"
                                class="form-control"
                                placeholder="https://github.com"
                            />
                        </div>
                        <div class="col-md-6">
                            <label for="sso-api-url" class="form-label">{{ $t("ssoAPIURL") }}</label>
                            <input
                                id="sso-api-url"
                                v-model.trim="form.apiURL"
                                type="url"
                                class="form-control"
                                placeholder="https://api.github.com"
                            />
                        </div>
                    </div>
                </details>

                <p class="form-text" data-testid="sso-last-login">
                    <template v-if="lastGitHubLogin">
                        {{ $t("ssoLastLogin", [lastGitHubLogin.username, lastGitHubLogin.at]) }}
                    </template>
                    <template v-else>{{ $t("ssoNoLoginYet") }}</template>
                </p>
            </fieldset>

            <!-- Passwords -->
            <h5 class="mt-4 settings-subheading">{{ $t("ssoPasswords") }}</h5>
            <div class="form-check form-switch mb-1">
                <input
                    id="sso-password-login"
                    v-model="form.passwordLogin"
                    class="form-check-input"
                    type="checkbox"
                    role="switch"
                    :disabled="locked || (form.passwordLogin && !canTurnOffPasswords)"
                    data-testid="sso-password-login"
                />
                <label class="form-check-label" for="sso-password-login">{{ $t("ssoPasswordLogin") }}</label>
            </div>
            <div class="form-text mb-4" data-testid="sso-password-help">
                <template v-if="!form.passwordLogin">{{ $t("ssoPasswordsOffHelp") }}</template>
                <template v-else-if="canTurnOffPasswords">{{ $t("ssoPasswordsCanTurnOff") }}</template>
                <template v-else>{{ $t("ssoPasswordsNeedGitHub") }}</template>
            </div>

            <button v-if="!locked" type="submit" class="btn btn-primary" :disabled="saving" data-testid="sso-save">
                {{ $t("Save") }}
            </button>
        </form>
    </div>
</template>

<script>
import CopyableInput from "../CopyableInput.vue";
import HiddenInput from "../HiddenInput.vue";
import { callbackURL, parseTeams, teamsText } from "../../sso-settings";

/**
 * Settings > Single Sign-On: logging in with GitHub, and whether passwords work.
 */
export default {
    components: { CopyableInput, HiddenInput },
    data() {
        return {
            loaded: false,
            saving: false,
            managedByEnv: false,
            hasClientSecret: false,
            youUseGitHub: false,
            serverCallbackURL: "",
            lastGitHubLogin: null,
            // The settings as saved, to tell what the form changes
            saved: null,
            form: {},
            adminTeams: "",
            viewerTeams: "",
        };
    },
    computed: {
        locked() {
            return this.managedByEnv;
        },
        callback() {
            return callbackURL(this.serverCallbackURL, location.origin);
        },

        /**
         * Whether this admin can turn passwords off: logged in with GitHub, with GitHub's settings
         * as saved, which the server checks again
         * @returns {boolean} Whether they can
         */
        canTurnOffPasswords() {
            const s = this.saved;
            return (
                this.youUseGitHub &&
                s?.githubEnabled &&
                this.form.githubEnabled &&
                this.form.clientId === s.clientId &&
                this.form.clientSecret === "" &&
                this.form.org === s.org &&
                teamsText(parseTeams(this.adminTeams)) === teamsText(s.adminTeams) &&
                teamsText(parseTeams(this.viewerTeams)) === teamsText(s.viewerTeams) &&
                (this.form.webURL || "") === (s.webURL || "") &&
                (this.form.apiURL || "") === (s.apiURL || "")
            );
        },
    },
    mounted() {
        this.load();
    },
    methods: {
        /**
         * Read the settings
         * @returns {void}
         */
        load() {
            this.$root.getSocket().emit("getLoginSettings", (res) => {
                if (!res.ok) {
                    this.$root.toastRes(res);
                    return;
                }
                const s = res.settings;
                this.saved = s;
                this.managedByEnv = res.managedByEnv;
                this.hasClientSecret = s.hasClientSecret;
                this.youUseGitHub = res.youUseGitHub;
                this.serverCallbackURL = res.callbackURL;
                this.lastGitHubLogin = res.lastGitHubLogin;
                this.form = {
                    githubEnabled: s.githubEnabled,
                    clientId: s.clientId,
                    clientSecret: "",
                    org: s.org,
                    webURL: s.webURL,
                    apiURL: s.apiURL,
                    passwordLogin: s.passwordLogin,
                };
                this.adminTeams = teamsText(s.adminTeams);
                this.viewerTeams = teamsText(s.viewerTeams);
                this.loaded = true;
            });
        },

        /**
         * Save the settings, which apply at once
         * @returns {void}
         */
        save() {
            this.saving = true;
            const settings = {
                ...this.form,
                adminTeams: parseTeams(this.adminTeams),
                viewerTeams: parseTeams(this.viewerTeams),
            };
            this.$root.getSocket().emit("saveLoginSettings", settings, (res) => {
                this.saving = false;
                this.$root.toastRes(res);
                if (res.ok) {
                    this.load();
                }
            });
        },
    },
};
</script>

<style lang="scss" scoped>
.setup-steps {
    padding-left: 1.2rem;
}

details summary {
    cursor: pointer;
}
</style>
