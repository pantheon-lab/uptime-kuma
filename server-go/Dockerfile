# Builds on the runner's platform and cross-compiles, so multi-arch images don't need emulation.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
RUN apk add --no-cache ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
ENV CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH
RUN go build -trimpath -ldflags='-s -w' -o /kuma-analytics ./cmd/kuma-analytics
RUN go build -trimpath -ldflags='-s -w' -o /kuma-import ./cmd/kuma-import

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /kuma-analytics /kuma-analytics
COPY --from=build /kuma-import /kuma-import
USER 65534:65534
ENTRYPOINT ["/kuma-analytics"]
