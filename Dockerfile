# kuma-analytics with the web UI from src/ embedded, plus kuma-import.
# Build from the repository root: docker build -t uptime-kuma .

# The frontend is platform independent, so it's built once on the build platform.
FROM --platform=$BUILDPLATFORM node:26-alpine AS frontend
WORKDIR /app
COPY package.json package-lock.json .npmrc ./
RUN npm ci --no-audit --no-fund
COPY index.html vite.config.mjs tsconfig.json ./
COPY public ./public
COPY src ./src
# A release passes its version, which the UI and the server then both report; otherwise it's package.json's
ARG VERSION=""
RUN if [ -n "$VERSION" ]; then npm pkg set version="$VERSION"; fi \
    && npm run build && node -p "require('./package.json').version" > /app/VERSION

# Builds on the build platform and cross-compiles, so multi-arch images don't need emulation.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS backend
RUN apk add --no-cache ca-certificates
WORKDIR /src
COPY server-go/go.mod server-go/go.sum ./
RUN go mod download
COPY server-go/ ./
COPY --from=frontend /app/dist ./internal/webui/dist
COPY --from=frontend /app/VERSION /VERSION
ARG TARGETOS TARGETARCH
ENV CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH
# The UI compares this version with its own.
RUN go build -trimpath -ldflags="-s -w -X main.version=$(cat /VERSION)" -o /kuma-analytics ./cmd/kuma-analytics
RUN go build -trimpath -ldflags='-s -w' -o /kuma-import ./cmd/kuma-import

# The same programs, plus the apprise command that the apprise notification provider runs.
# Python makes this image much larger, so it's a separate one, published with an -apprise tag
# suffix: docker build --target apprise -t uptime-kuma:apprise .
FROM python:3.14-alpine3.24 AS apprise
RUN pip install --no-cache-dir apprise==2.1.0 \
    && apprise --version
COPY --from=backend /kuma-analytics /kuma-analytics
COPY --from=backend /kuma-import /kuma-import
USER 65534:65534
ENTRYPOINT ["/kuma-analytics"]

# The default image, the last stage: only the two programs and CA certificates.
FROM scratch AS default
COPY --from=backend /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=backend /kuma-analytics /kuma-analytics
COPY --from=backend /kuma-import /kuma-import
USER 65534:65534
ENTRYPOINT ["/kuma-analytics"]
