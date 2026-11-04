FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /kuma-analytics ./cmd/kuma-analytics

FROM scratch
COPY --from=build /kuma-analytics /kuma-analytics
USER 65534:65534
ENTRYPOINT ["/kuma-analytics"]
