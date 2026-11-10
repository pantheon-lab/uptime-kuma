import { createAuthClient } from "better-auth/vue";
import { twoFactorClient, usernameClient } from "better-auth/client/plugins";
import { apiKeyClient } from "@better-auth/api-key/client";
import { reconnectSocket } from "./mixins/socket";

// The origin that served the page. `npm run dev` proxies the backend's paths to it.
export const baseURL = "";

export const authClient = createAuthClient({
    baseURL,
    plugins: [usernameClient(), twoFactorClient(), apiKeyClient()],
});
/**
 * @returns Check if the user is logged in
 */
export async function isLoggedIn() {
    const session = await authClient.getSession();
    return session.data !== null;
}

/**
 * @param username Username
 * @param password Password
 * @param remember Remember Me
 */
export async function login(username: string, password: string, remember: boolean = true) {
    const { data, error } = await authClient.signIn.username({
        username,
        password,
        rememberMe: remember,
    });

    if (error) {
        throw new Error(error.message);
    }

    if ("twoFactorRedirect" in data) {
        return "twoFactorRequired";
    }

    reconnectSocket();
}

/**
 * @param token Token
 */
export async function verifyTotp(token: string) {
    const { error } = await authClient.twoFactor.verifyTotp({
        code: token,
    });

    if (error) {
        throw new Error(error.message);
    }

    reconnectSocket();
}

/**
 * @param onSuccess
 */
export async function logout(onSuccess = () => {}) {
    await authClient.signOut({
        fetchOptions: {
            onSuccess,
        },
    });
}
