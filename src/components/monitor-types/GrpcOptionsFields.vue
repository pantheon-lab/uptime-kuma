<template>
    <!-- Proto service enable TLS -->
    <h2 class="mt-5 mb-2">{{ $t("GRPC Options") }}</h2>
    <div class="my-3 form-check">
        <input id="grpc-enable-tls" v-model="monitor.grpcEnableTls" class="form-check-input" type="checkbox" value="" />
        <label class="form-check-label" for="grpc-enable-tls">
            {{ $t("Enable TLS") }}
        </label>
        <div class="form-text">
            {{ $t("enableGRPCTls") }}
        </div>
    </div>
    <!-- Proto service name data -->
    <div class="my-3">
        <label for="protobuf" class="form-label">{{ $t("Proto Service Name") }}</label>
        <input
            id="name"
            v-model="monitor.grpcServiceName"
            type="text"
            class="form-control"
            :placeholder="protoServicePlaceholder"
            required
        />
    </div>

    <!-- Proto method data -->
    <div class="my-3">
        <label for="protobuf" class="form-label">{{ $t("Proto Method") }}</label>
        <input
            id="name"
            v-model="monitor.grpcMethod"
            type="text"
            class="form-control"
            :placeholder="protoMethodPlaceholder"
            required
        />
        <div class="form-text">
            {{ $t("grpcMethodDescription") }}
        </div>
    </div>

    <!-- Proto data -->
    <div class="my-3">
        <label for="protobuf" class="form-label">{{ $t("Proto Content") }}</label>
        <textarea
            id="protobuf"
            v-model="monitor.grpcProtobuf"
            class="form-control"
            :placeholder="protoBufDataPlaceholder"
        ></textarea>
    </div>

    <!-- Body -->
    <div class="my-3">
        <label for="body" class="form-label">{{ $t("Body") }}</label>
        <textarea id="body" v-model="monitor.grpcBody" class="form-control" :placeholder="bodyPlaceholder"></textarea>
    </div>
</template>

<script>
/**
 * The Edit Monitor form's fields for gRPC monitors: TLS, service, method and body. They edit the form's monitor, which
 * EditMonitor provides.
 */
export default {
    inject: ["editMonitor"],
    computed: {
        monitor() {
            return this.editMonitor.monitor;
        },
        bodyPlaceholder() {
            return this.editMonitor.bodyPlaceholder;
        },
        protoBufDataPlaceholder() {
            return this.editMonitor.protoBufDataPlaceholder;
        },
        protoMethodPlaceholder() {
            return this.editMonitor.protoMethodPlaceholder;
        },
        protoServicePlaceholder() {
            return this.editMonitor.protoServicePlaceholder;
        },
    },
};
</script>
