<template>
    <div class="my-3">
        <label for="system-service-name" class="form-label">
            {{ $t("systemServiceName") }}
        </label>
        <input
            id="system-service-name"
            v-model="monitor.config.systemServiceName"
            type="text"
            class="form-control"
            required
            :placeholder="$root.info.runtime.platform === 'win32' ? 'Dnscache' : 'nginx'"
        />

        <div class="form-text">
            <template v-if="$root.info.runtime.platform === 'linux'">
                {{
                    $t("systemServiceDescriptionLinux", {
                        service_name: monitor.config.systemServiceName || "nginx",
                    })
                }}
            </template>
            <template v-else-if="$root.info.runtime.platform === 'win32'">
                {{
                    $t("systemServiceDescriptionWindows", {
                        service_name: monitor.config.systemServiceName || "Dnscache",
                    })
                }}
            </template>
            <template v-else>
                {{
                    $t("systemServiceDescription", {
                        service_name: monitor.config.systemServiceName || "nginx",
                    })
                }}
            </template>

            <template
                v-if="
                    !monitor.config.systemServiceName ||
                    /^[a-zA-Z0-9_\-\.\@\ ]+$/.test(monitor.config.systemServiceName)
                "
            >
                <div v-if="$root.info.runtime.platform === 'linux'" class="mt-2">
                    <div>
                        <i18n-t keypath="systemServiceCommandHint" tag="span">
                            <template #command>
                                <code>
                                    systemctl is-active
                                    {{ monitor.config.systemServiceName || "nginx" }}
                                </code>
                            </template>
                        </i18n-t>
                    </div>
                    <div class="text-secondary small">
                        {{ $t("systemServiceExpectedOutput", ["active"]) }}
                    </div>
                </div>
                <div v-else-if="$root.info.runtime.platform === 'win32'" class="mt-2">
                    <div>
                        <i18n-t keypath="systemServiceCommandHint" tag="span">
                            <template #command>
                                <code>
                                    (Get-Service -Name '{{
                                        (monitor.config.systemServiceName || "Dnscache").replaceAll("'", "''")
                                    }}').Status
                                </code>
                            </template>
                        </i18n-t>
                    </div>
                    <div class="text-secondary small">
                        {{ $t("systemServiceExpectedOutput", ["Running"]) }}
                    </div>
                </div>
            </template>
        </div>
    </div>
</template>

<script>
/**
 * The Edit Monitor form's fields for system service monitors: the service's name. They edit the form's monitor, which
 * EditMonitor provides.
 */
export default {
    inject: ["editMonitor"],
    computed: {
        monitor() {
            return this.editMonitor.monitor;
        },
    },
};
</script>
