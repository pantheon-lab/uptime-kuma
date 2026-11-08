<template>
    <h2 class="mt-5 mb-2">{{ $t("Authentication") }}</h2>

    <!-- Auth Method -->
    <div class="my-3">
        <label for="ws-auth-method" class="form-label">{{ $t("Method") }}</label>
        <select id="ws-auth-method" v-model="monitor.authMethod" class="form-select">
            <option :value="null">
                {{ $t("None") }}
            </option>
            <option value="basic">
                {{ $t("HTTP Basic Auth") }}
            </option>
            <option value="bearer">
                {{ $t("Bearer Token") }}
            </option>
            <option value="oauth2-cc">
                {{ $t("OAuth2: Client Credentials") }}
            </option>
            <option value="mtls">mTLS</option>
        </select>
    </div>

    <template v-if="monitor.authMethod === 'basic'">
        <div class="my-3">
            <label for="ws-basicauth-user" class="form-label">{{ $t("Username") }}</label>
            <input
                id="ws-basicauth-user"
                v-model="monitor.basic_auth_user"
                type="text"
                class="form-control"
                :placeholder="$t('Username')"
            />
        </div>
        <div class="my-3">
            <label for="ws-basicauth-pass" class="form-label">{{ $t("Password") }}</label>
            <HiddenInput
                id="ws-basicauth-pass"
                v-model="monitor.basic_auth_pass"
                autocomplete="new-password"
                :placeholder="$t('Password')"
            />
        </div>
    </template>

    <template v-else-if="monitor.authMethod === 'bearer'">
        <div class="my-3">
            <label for="ws-bearer-token" class="form-label">{{ $t("Token") }}</label>
            <HiddenInput
                id="ws-bearer-token"
                v-model="monitor.bearer_token"
                autocomplete="new-password"
                :placeholder="$t('Token')"
            />
        </div>
    </template>

    <template v-else-if="monitor.authMethod === 'oauth2-cc'">
        <div class="my-3">
            <label for="ws-oauth-auth-method" class="form-label">
                {{ $t("Authentication Method") }}
            </label>
            <select id="ws-oauth-auth-method" v-model="monitor.oauth_auth_method" class="form-select">
                <option value="client_secret_basic">
                    {{ $t("Authorization Header") }}
                </option>
                <option value="client_secret_post">
                    {{ $t("Form Data Body") }}
                </option>
            </select>
        </div>
        <div class="my-3">
            <label for="ws-oauth-token-url" class="form-label">
                {{ $t("OAuth Token URL") }}
            </label>
            <input
                id="ws-oauth-token-url"
                v-model="monitor.oauth_token_url"
                type="text"
                class="form-control"
                :placeholder="$t('OAuth Token URL')"
                required
            />
        </div>
        <div class="my-3">
            <label for="ws-oauth-client-id" class="form-label">
                {{ $t("Client ID") }}
            </label>
            <input
                id="ws-oauth-client-id"
                v-model="monitor.oauth_client_id"
                type="text"
                class="form-control"
                :placeholder="$t('Client ID')"
                required
            />
        </div>
        <template
            v-if="
                monitor.oauth_auth_method === 'client_secret_post' ||
                monitor.oauth_auth_method === 'client_secret_basic'
            "
        >
            <div class="my-3">
                <label for="ws-oauth-client-secret" class="form-label">
                    {{ $t("Client Secret") }}
                </label>
                <HiddenInput
                    id="ws-oauth-client-secret"
                    v-model="monitor.oauth_client_secret"
                    :placeholder="$t('Client Secret')"
                    :required="true"
                />
            </div>
            <div class="my-3">
                <label for="ws-oauth-scopes" class="form-label">
                    {{ $t("OAuth Scope") }}
                </label>
                <input
                    id="ws-oauth-scopes"
                    v-model="monitor.oauth_scopes"
                    type="text"
                    class="form-control"
                    :placeholder="$t('Optional: Space separated list of scopes')"
                />
            </div>
            <div class="my-3">
                <label for="ws-oauth-audience" class="form-label">
                    {{ $t("OAuth Audience") }}
                </label>
                <input
                    id="ws-oauth-audience"
                    v-model="monitor.oauth_audience"
                    type="text"
                    class="form-control"
                    :placeholder="$t('Optional: The audience to request the JWT for')"
                />
            </div>
        </template>
    </template>

    <template v-else-if="monitor.authMethod === 'mtls'">
        <div class="my-3">
            <label for="ws-tls-cert" class="form-label">
                {{ $t("mtls-auth-server-cert-label") }}
            </label>
            <textarea
                id="ws-tls-cert"
                v-model="monitor.tlsCert"
                class="form-control"
                :placeholder="$t('mtls-auth-server-cert-placeholder')"
                required
            ></textarea>
        </div>
        <div class="my-3">
            <label for="ws-tls-key" class="form-label">
                {{ $t("mtls-auth-server-key-label") }}
            </label>
            <textarea
                id="ws-tls-key"
                v-model="monitor.tlsKey"
                class="form-control"
                :placeholder="$t('mtls-auth-server-key-placeholder')"
                required
            ></textarea>
        </div>
        <div class="my-3">
            <label for="ws-tls-ca" class="form-label">
                {{ $t("mtls-auth-server-ca-label") }}
            </label>
            <textarea
                id="ws-tls-ca"
                v-model="monitor.tlsCa"
                class="form-control"
                :placeholder="$t('mtls-auth-server-ca-placeholder')"
            ></textarea>
        </div>
    </template>
</template>

<script>
import HiddenInput from "../HiddenInput.vue";

/**
 * The Edit Monitor form's fields for WebSocket Upgrade monitors: authentication. They edit the form's monitor, which
 * EditMonitor provides.
 */
export default {
    components: {
        HiddenInput,
    },
    inject: ["editMonitor"],
    computed: {
        monitor() {
            return this.editMonitor.monitor;
        },
    },
};
</script>
