<template>
    <div class="my-3">
        <label for="ssh_username" class="form-label">{{ $t("Username") }}</label>
        <input
            id="ssh_username"
            v-model="monitor.sshUsername"
            type="text"
            class="form-control"
            required
            autocomplete="username"
        />
    </div>

    <!-- Auth Method -->
    <div class="my-3">
        <label for="ssh_auth_method" class="form-label">
            {{ $t("Authentication Method") }}
        </label>
        <select id="ssh_auth_method" v-model="monitor.sshAuthMethod" class="form-select">
            <option value="password">{{ $t("Password") }}</option>
            <option value="privateKey">{{ $t("SSH Private Key") }}</option>
        </select>
    </div>

    <!-- Password auth -->
    <div v-if="monitor.sshAuthMethod !== 'privateKey'" class="my-3">
        <label for="ssh_password" class="form-label">{{ $t("Password") }}</label>
        <HiddenInput
            id="ssh_password"
            v-model="monitor.sshPassword"
            autocomplete="current-password"
            required="true"
        ></HiddenInput>
    </div>

    <!-- SSH Key auth -->
    <template v-if="monitor.sshAuthMethod === 'privateKey'">
        <div class="my-3">
            <label for="ssh_private_key" class="form-label">
                {{ $t("SSH Private Key") }}
            </label>
            <textarea
                id="ssh_private_key"
                v-model="monitor.sshPrivateKey"
                class="form-control"
                rows="6"
                :placeholder="$t('sshPrivateKeyPlaceholder')"
                required
                autocomplete="off"
            ></textarea>
            <div class="form-text">{{ $t("sshPrivateKeyHelpText") }}</div>
        </div>
        <div class="my-3">
            <label for="ssh_passphrase" class="form-label">
                {{ $t("Passphrase") }}
                <span class="text-muted small">({{ $t("optional") }})</span>
            </label>
            <HiddenInput id="ssh_passphrase" v-model="monitor.sshPassphrase" autocomplete="off"></HiddenInput>
            <div class="form-text">{{ $t("sshPassphraseHelpText") }}</div>
        </div>
    </template>

    <div class="my-3">
        <label for="sftp_path" class="form-label">{{ $t("SFTP Path to Check") }}</label>
        <input
            id="sftp_path"
            v-model="monitor.sftpPath"
            type="text"
            class="form-control"
            placeholder="/path/to/check (optional)"
        />
        <div class="form-text">{{ $t("sftpPathHelpText") }}</div>
    </div>
</template>

<script>
import HiddenInput from "../HiddenInput.vue";

/**
 * The Edit Monitor form's fields for SFTP monitors: login and path. They edit the form's monitor, which
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
