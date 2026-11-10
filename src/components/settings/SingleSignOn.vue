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
                    <template v-if="lastLogins.github">
                        {{ $t("ssoLastLogin", [lastLogins.github.username, lastLogins.github.at]) }}
                    </template>
                    <template v-else>{{ $t("ssoNoLoginYet") }}</template>
                </p>
            </fieldset>

            <!-- Google -->
            <h5 class="mt-4 settings-subheading">{{ $t("ssoGoogle") }}</h5>
            <div class="form-check form-switch mb-3">
                <input
                    id="sso-google-enabled"
                    v-model="google.enabled"
                    class="form-check-input"
                    type="checkbox"
                    role="switch"
                    :disabled="locked"
                    data-testid="sso-google-enabled"
                />
                <label class="form-check-label" for="sso-google-enabled">{{ $t("ssoGoogleEnabled") }}</label>
                <div class="form-text">{{ $t("ssoGoogleHelp") }}</div>
            </div>

            <fieldset v-if="google.enabled" :disabled="locked" class="ms-md-4 mb-4" data-testid="sso-google">
                <ol class="setup-steps form-text">
                    <li>{{ $t("ssoGoogleStepRegister") }}</li>
                    <li>{{ $t("ssoGoogleStepFill") }}</li>
                    <li>{{ $t("ssoGoogleStepTry") }}</li>
                </ol>

                <div class="mb-3">
                    <label for="sso-google-callback" class="form-label">{{ $t("ssoRedirectURI") }}</label>
                    <CopyableInput
                        id="sso-google-callback"
                        :model-value="googleCallback.url"
                        data-testid="sso-google-callback"
                    />
                    <div v-if="googleCallback.guessed" class="form-text text-warning-emphasis">
                        {{ $t("ssoCallbackGuessed") }}
                    </div>
                </div>

                <div class="row g-3 mb-3">
                    <div class="col-md-6">
                        <label for="sso-google-client-id" class="form-label">{{ $t("ssoClientID") }}</label>
                        <input
                            id="sso-google-client-id"
                            v-model.trim="google.clientId"
                            type="text"
                            class="form-control"
                            autocomplete="off"
                            required
                            placeholder="…apps.googleusercontent.com"
                            data-testid="sso-google-client-id"
                        />
                    </div>
                    <div class="col-md-6">
                        <label for="sso-google-client-secret" class="form-label">{{ $t("ssoClientSecret") }}</label>
                        <HiddenInput
                            id="sso-google-client-secret"
                            v-model="google.clientSecret"
                            :placeholder="saved?.google?.hasClientSecret ? $t('ssoSecretKept') : ''"
                        />
                    </div>
                </div>

                <div class="mb-3">
                    <label for="sso-google-admins" class="form-label">{{ $t("ssoGoogleAdmins") }}</label>
                    <input
                        id="sso-google-admins"
                        v-model="googleAdmins"
                        type="text"
                        class="form-control"
                        placeholder="ann@example.com, bob@example.com"
                        data-testid="sso-google-admins"
                    />
                </div>
                <div class="mb-3">
                    <label for="sso-google-viewers" class="form-label">{{ $t("ssoGoogleViewers") }}</label>
                    <input
                        id="sso-google-viewers"
                        v-model="googleViewers"
                        type="text"
                        class="form-control"
                        placeholder="*, support@example.com"
                        data-testid="sso-google-viewers"
                    />
                    <div class="form-text">{{ $t("ssoGoogleViewersHelp") }}</div>
                </div>
                <div class="mb-3">
                    <label for="sso-google-domains" class="form-label">{{ $t("ssoGoogleDomains") }}</label>
                    <input
                        id="sso-google-domains"
                        v-model="googleDomains"
                        type="text"
                        class="form-control"
                        placeholder="example.com"
                        data-testid="sso-google-domains"
                    />
                    <div class="form-text">{{ $t("ssoGoogleDomainsHelp") }}</div>
                </div>

                <p class="form-text" data-testid="sso-google-last-login">
                    <template v-if="lastLogins.google">
                        {{ $t("ssoLastGoogleLogin", [lastLogins.google.username, lastLogins.google.at]) }}
                    </template>
                    <template v-else>{{ $t("ssoNoGoogleLoginYet") }}</template>
                </p>
            </fieldset>

            <!-- OpenID Connect -->
            <h5 class="mt-4 settings-subheading">{{ $t("ssoOIDC") }}</h5>
            <div class="form-check form-switch mb-3">
                <input
                    id="sso-oidc-enabled"
                    v-model="oidc.enabled"
                    class="form-check-input"
                    type="checkbox"
                    role="switch"
                    :disabled="locked"
                    data-testid="sso-oidc-enabled"
                />
                <label class="form-check-label" for="sso-oidc-enabled">{{ $t("ssoOIDCEnabled") }}</label>
                <div class="form-text">{{ $t("ssoOIDCHelp") }}</div>
            </div>

            <fieldset v-if="oidc.enabled" :disabled="locked" class="ms-md-4 mb-4" data-testid="sso-oidc">
                <ol class="setup-steps form-text">
                    <li>{{ $t("ssoOIDCStepRegister") }}</li>
                    <li>{{ $t("ssoOIDCStepFill") }}</li>
                    <li>{{ $t("ssoOIDCStepTry") }}</li>
                </ol>

                <div class="mb-3">
                    <label for="sso-oidc-callback" class="form-label">{{ $t("ssoRedirectURI") }}</label>
                    <CopyableInput
                        id="sso-oidc-callback"
                        :model-value="oidcCallback.url"
                        data-testid="sso-oidc-callback"
                    />
                    <div v-if="oidcCallback.guessed" class="form-text text-warning-emphasis">
                        {{ $t("ssoCallbackGuessed") }}
                    </div>
                </div>

                <div class="row g-3 mb-3">
                    <div class="col-md-4">
                        <label for="sso-oidc-name" class="form-label">{{ $t("ssoOIDCName") }}</label>
                        <input
                            id="sso-oidc-name"
                            v-model.trim="oidc.name"
                            type="text"
                            class="form-control"
                            required
                            maxlength="40"
                            placeholder="Okta"
                            data-testid="sso-oidc-name"
                        />
                    </div>
                    <div class="col-md-8">
                        <label for="sso-oidc-issuer" class="form-label">{{ $t("ssoOIDCIssuer") }}</label>
                        <div class="input-group">
                            <input
                                id="sso-oidc-issuer"
                                v-model.trim="oidc.issuer"
                                type="url"
                                class="form-control"
                                required
                                placeholder="https://example.okta.com"
                                data-testid="sso-oidc-issuer"
                            />
                            <button
                                type="button"
                                class="btn btn-outline-normal"
                                :disabled="!oidc.issuer || checking"
                                data-testid="sso-oidc-check"
                                @click="checkIssuer"
                            >
                                {{ $t("ssoOIDCCheck") }}
                            </button>
                        </div>
                        <div class="form-text" role="status" aria-live="polite" data-testid="sso-oidc-check-result">
                            <span v-if="issuerCheck && issuerCheck.ok" class="text-success-emphasis">
                                {{ $t("ssoOIDCCheckOK", [issuerCheck.issuer]) }}
                            </span>
                            <span v-else-if="issuerCheck" class="text-danger">{{ issuerCheck.msg }}</span>
                            <template v-else>{{ $t("ssoOIDCIssuerHelp") }}</template>
                        </div>
                    </div>
                </div>

                <div class="row g-3 mb-3">
                    <div class="col-md-6">
                        <label for="sso-oidc-client-id" class="form-label">{{ $t("ssoClientID") }}</label>
                        <input
                            id="sso-oidc-client-id"
                            v-model.trim="oidc.clientId"
                            type="text"
                            class="form-control"
                            autocomplete="off"
                            required
                            data-testid="sso-oidc-client-id"
                        />
                    </div>
                    <div class="col-md-6">
                        <label for="sso-oidc-client-secret" class="form-label">{{ $t("ssoClientSecret") }}</label>
                        <HiddenInput
                            id="sso-oidc-client-secret"
                            v-model="oidc.clientSecret"
                            :maxlength="512"
                            :placeholder="saved?.oidc?.hasClientSecret ? $t('ssoSecretKept') : ''"
                        />
                    </div>
                </div>

                <div class="row g-3 mb-1">
                    <div class="col-md-6">
                        <label for="sso-oidc-admin-groups" class="form-label">{{ $t("ssoOIDCAdminGroups") }}</label>
                        <input
                            id="sso-oidc-admin-groups"
                            v-model="oidcAdminGroups"
                            type="text"
                            class="form-control"
                            placeholder="Kuma Admins"
                            data-testid="sso-oidc-admin-groups"
                        />
                    </div>
                    <div class="col-md-6">
                        <label for="sso-oidc-viewer-groups" class="form-label">{{ $t("ssoOIDCViewerGroups") }}</label>
                        <input
                            id="sso-oidc-viewer-groups"
                            v-model="oidcViewerGroups"
                            type="text"
                            class="form-control"
                            placeholder="Support, Engineering"
                            data-testid="sso-oidc-viewer-groups"
                        />
                    </div>
                </div>
                <div class="form-text mb-3">{{ $t("ssoOIDCGroupsHelp") }}</div>

                <div class="row g-3 mb-1">
                    <div class="col-md-6">
                        <label for="sso-oidc-admin-emails" class="form-label">{{ $t("ssoGoogleAdmins") }}</label>
                        <input
                            id="sso-oidc-admin-emails"
                            v-model="oidcAdminEmails"
                            type="text"
                            class="form-control"
                            data-testid="sso-oidc-admin-emails"
                        />
                    </div>
                    <div class="col-md-6">
                        <label for="sso-oidc-viewer-emails" class="form-label">{{ $t("ssoGoogleViewers") }}</label>
                        <input
                            id="sso-oidc-viewer-emails"
                            v-model="oidcViewerEmails"
                            type="text"
                            class="form-control"
                            data-testid="sso-oidc-viewer-emails"
                        />
                    </div>
                </div>
                <div class="form-text mb-3">{{ $t("ssoOIDCEmailsHelp") }}</div>

                <details class="mb-3" :open="!!(oidc.groupsClaim || oidcScopes)">
                    <summary>{{ $t("ssoOIDCAdvanced") }}</summary>
                    <div class="row g-3 mt-1">
                        <div class="col-md-6">
                            <label for="sso-oidc-scopes" class="form-label">{{ $t("ssoOIDCScopes") }}</label>
                            <input
                                id="sso-oidc-scopes"
                                v-model="oidcScopes"
                                type="text"
                                class="form-control"
                                placeholder="email, profile"
                                data-testid="sso-oidc-scopes"
                            />
                            <div class="form-text">{{ $t("ssoOIDCScopesHelp") }}</div>
                        </div>
                        <div class="col-md-6">
                            <label for="sso-oidc-groups-claim" class="form-label">{{ $t("ssoOIDCGroupsClaim") }}</label>
                            <input
                                id="sso-oidc-groups-claim"
                                v-model.trim="oidc.groupsClaim"
                                type="text"
                                class="form-control"
                                placeholder="groups"
                                data-testid="sso-oidc-groups-claim"
                            />
                            <div class="form-text">{{ $t("ssoOIDCGroupsClaimHelp") }}</div>
                        </div>
                    </div>
                </details>

                <p class="form-text" data-testid="sso-oidc-last-login">
                    <template v-if="lastLogins.oidc">
                        {{ $t("ssoLastOIDCLogin", [oidc.name, lastLogins.oidc.username, lastLogins.oidc.at]) }}
                    </template>
                    <template v-else>{{ $t("ssoNoOIDCLoginYet", [oidc.name || $t("ssoOIDC")]) }}</template>
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
                <template v-else>{{ $t("ssoPasswordsNeedSSO") }}</template>
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
import { callbackURL, parseGroups, parseList, parseTeams, teamsText } from "../../sso-settings";

/**
 * Settings > Single Sign-On: logging in with GitHub, Google or an OpenID Connect provider, and whether
 * passwords work.
 */
export default {
    components: { CopyableInput, HiddenInput },
    data() {
        return {
            loaded: false,
            saving: false,
            managedByEnv: false,
            hasClientSecret: false,
            // How this admin logged in: "password", "github", "google" or "oidc"
            youUse: "password",
            serverCallbackURL: "",
            serverGoogleCallbackURL: "",
            serverOIDCCallbackURL: "",
            lastLogins: {},
            // The settings as saved, to tell what the form changes
            saved: null,
            form: {},
            adminTeams: "",
            viewerTeams: "",
            google: {},
            googleAdmins: "",
            googleViewers: "",
            googleDomains: "",
            oidc: {},
            oidcScopes: "",
            oidcAdminGroups: "",
            oidcViewerGroups: "",
            oidcAdminEmails: "",
            oidcViewerEmails: "",
            checking: false,
            // The issuer check's answer: { ok, issuer } or { ok: false, msg }
            issuerCheck: null,
        };
    },
    computed: {
        locked() {
            return this.managedByEnv;
        },
        callback() {
            return callbackURL(this.serverCallbackURL, location.origin);
        },
        googleCallback() {
            return callbackURL(this.serverGoogleCallbackURL, location.origin, "google");
        },
        oidcCallback() {
            return callbackURL(this.serverOIDCCallbackURL, location.origin, "oidc");
        },

        /**
         * Whether this admin can turn passwords off: logged in with GitHub or Google, with that
         * provider's settings as saved, which the server checks again
         * @returns {boolean} Whether they can
         */
        canTurnOffPasswords() {
            if (this.youUse === "oidc") {
                const o = this.saved?.oidc;
                const same = (text, list, parse) => teamsText(parse(text)) === teamsText(list);
                return (
                    !!o?.enabled &&
                    this.oidc.enabled &&
                    this.oidc.issuer.replace(/\/+$/, "") === o.issuer &&
                    this.oidc.clientId === o.clientId &&
                    this.oidc.clientSecret === "" &&
                    (this.oidc.groupsClaim || "") === (o.groupsClaim || "") &&
                    same(this.oidcScopes, o.scopes, parseGroups) &&
                    same(this.oidcAdminGroups, o.adminGroups, parseGroups) &&
                    same(this.oidcViewerGroups, o.viewerGroups, parseGroups) &&
                    same(this.oidcAdminEmails, o.adminEmails, parseList) &&
                    same(this.oidcViewerEmails, o.viewerEmails, parseList)
                );
            }
            if (this.youUse === "google") {
                const g = this.saved?.google;
                return (
                    !!g?.enabled &&
                    this.google.enabled &&
                    this.google.clientId === g.clientId &&
                    this.google.clientSecret === "" &&
                    teamsText(parseList(this.googleAdmins)) === teamsText(g.adminEmails) &&
                    teamsText(parseList(this.googleViewers)) === teamsText(g.viewerEmails) &&
                    teamsText(parseList(this.googleDomains)) === teamsText(g.domains)
                );
            }
            const s = this.saved;
            return (
                this.youUse === "github" &&
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
                this.youUse = res.youUse;
                this.serverCallbackURL = res.callbackURL;
                this.serverGoogleCallbackURL = res.googleCallbackURL;
                this.serverOIDCCallbackURL = res.oidcCallbackURL;
                const o = s.oidc;
                this.oidc = {
                    enabled: o.enabled,
                    name: o.name,
                    issuer: o.issuer,
                    clientId: o.clientId,
                    clientSecret: "",
                    groupsClaim: o.groupsClaim,
                };
                this.oidcScopes = teamsText(o.scopes);
                this.oidcAdminGroups = teamsText(o.adminGroups);
                this.oidcViewerGroups = teamsText(o.viewerGroups);
                this.oidcAdminEmails = teamsText(o.adminEmails);
                this.oidcViewerEmails = teamsText(o.viewerEmails);
                this.issuerCheck = null;
                this.lastLogins = res.lastLogins || {};
                this.google = {
                    enabled: s.google.enabled,
                    clientId: s.google.clientId,
                    clientSecret: "",
                };
                this.googleAdmins = teamsText(s.google.adminEmails);
                this.googleViewers = teamsText(s.google.viewerEmails);
                this.googleDomains = teamsText(s.google.domains);
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
         * Check that the issuer's discovery document can be read
         * @returns {void}
         */
        checkIssuer() {
            this.checking = true;
            this.$root.getSocket().emit("checkOIDCIssuer", this.oidc.issuer, (res) => {
                this.checking = false;
                this.issuerCheck = res;
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
                google: {
                    ...this.google,
                    adminEmails: parseList(this.googleAdmins),
                    viewerEmails: parseList(this.googleViewers),
                    domains: parseList(this.googleDomains),
                },
                oidc: {
                    ...this.oidc,
                    scopes: parseGroups(this.oidcScopes.replace(/\s+/g, ",")),
                    adminGroups: parseGroups(this.oidcAdminGroups),
                    viewerGroups: parseGroups(this.oidcViewerGroups),
                    adminEmails: parseList(this.oidcAdminEmails),
                    viewerEmails: parseList(this.oidcViewerEmails),
                },
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
