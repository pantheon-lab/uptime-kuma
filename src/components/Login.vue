<template>
    <div class="form-container">
        <div class="form">
            <div v-if="loginError" class="alert alert-danger mt-3" role="alert">
                {{ loginError }}
            </div>
            <form v-if="passwordLogin" aria-label="Login Form" class="pt-3" @submit.prevent="submit">
                <div v-if="!tokenRequired" class="form-floating">
                    <input
                        id="floatingInput"
                        v-model="username"
                        type="text"
                        class="form-control"
                        placeholder="Username"
                        autocomplete="username"
                        required
                    />
                    <label for="floatingInput">{{ $t("Username") }}</label>
                </div>

                <div v-if="!tokenRequired" class="form-floating mt-3">
                    <input
                        id="floatingPassword"
                        v-model="password"
                        type="password"
                        class="form-control"
                        placeholder="Password"
                        autocomplete="current-password"
                        required
                    />
                    <label for="floatingPassword">{{ $t("Password") }}</label>
                </div>

                <div v-if="tokenRequired">
                    <div class="form-floating mt-3">
                        <input
                            id="otp"
                            ref="otpInput"
                            v-model="token"
                            type="text"
                            maxlength="6"
                            class="form-control"
                            placeholder="123456"
                            autocomplete="one-time-code"
                            required
                        />
                        <label for="otp">{{ $t("Token") }}</label>
                    </div>
                </div>

                <div class="form-check mb-3 mt-3 d-flex justify-content-center pe-4">
                    <div class="form-check">
                        <input
                            id="remember"
                            v-model="$root.remember"
                            type="checkbox"
                            value="remember-me"
                            class="form-check-input"
                        />

                        <label class="form-check-label" for="remember">
                            {{ $t("Remember me") }}
                        </label>
                    </div>
                </div>
                <button class="w-100 btn btn-primary" type="submit" :disabled="processing">
                    {{ $t("Login") }}
                </button>

                <div v-if="res && !res.ok" class="alert alert-danger mt-3" role="alert">
                    {{ $t(res.msg) }}
                </div>
            </form>
            <div v-if="$root.info.githubLogin && !tokenRequired" class="pt-3">
                <div v-if="!passwordLogin" class="form-check mb-3 d-flex justify-content-center pe-4">
                    <div class="form-check">
                        <input id="remember-github" v-model="$root.remember" type="checkbox" class="form-check-input" />
                        <label class="form-check-label" for="remember-github">
                            {{ $t("Remember me") }}
                        </label>
                    </div>
                </div>
                <a id="github-login" class="w-100 btn btn-dark" :href="githubLoginURL">
                    {{ $t("loginWithGitHub") }}
                </a>
            </div>
        </div>
    </div>
</template>

<script>
import { login, verifyTotp } from "../auth-client";

export default {
    data() {
        return {
            processing: false,
            username: "",
            password: "",
            token: "",
            res: null,
            tokenRequired: false,
        };
    },

    computed: {
        /**
         * Whether logging in with a password is on
         * @returns {boolean} On
         */
        passwordLogin() {
            return this.$root.info.passwordLogin !== false;
        },
        /**
         * Where "Log in with GitHub" starts
         * @returns {string} Path
         */
        githubLoginURL() {
            return "/api/auth/github/start?remember=" + (this.$root.remember ? "1" : "0");
        },
        /**
         * Why the last GitHub login failed, from the address it came back to
         * @returns {string} Message, or empty
         */
        loginError() {
            const code = this.$route.query.loginError;
            const messages = {
                "github-not-allowed": this.$t("githubLoginNotAllowed"),
                "github-denied": this.$t("githubLoginDenied"),
                "github-expired": this.$t("githubLoginExpired"),
                "github-failed": this.$t("githubLoginFailed"),
            };
            return code ? messages[code] || this.$t("githubLoginFailed") : "";
        },
    },

    watch: {
        tokenRequired(newVal) {
            if (newVal) {
                this.$nextTick(() => {
                    this.$refs.otpInput?.focus();
                });
            }
        },
    },

    mounted() {
        document.title += " - Login";
    },

    unmounted() {
        document.title = document.title.replace(" - Login", "");
    },

    methods: {
        /**
         * Submit the user details and attempt to log in
         * @returns {void}
         */
        async submit() {
            this.processing = true;

            try {
                if (this.tokenRequired) {
                    await verifyTotp(this.token);
                    return;
                }

                const result = await login(this.username, this.password, this.$root.remember);
                if (result === "twoFactorRequired") {
                    this.tokenRequired = true;
                }
            } catch (e) {
                this.res = { ok: false, msg: e.message };
            } finally {
                this.processing = false;
            }
        },
    },
};
</script>

<style lang="scss" scoped>
.form-container {
    display: flex;
    align-items: center;
    padding-top: 40px;
    padding-bottom: 40px;
}

.form-floating {
    > label {
        padding-left: 1.3rem;
    }

    > .form-control {
        padding-left: 1.3rem;
    }
}

.form {
    width: 100%;
    max-width: 330px;
    padding: 15px;
    margin: auto;
    text-align: center;
}
</style>
