# 基础镜像由 workspace 发布流程替换为 ACR 内网不可变 digest；默认值保留本地构建能力。
ARG GO_BUILDER_IMAGE=golang:1.26-alpine
ARG ALPINE_RUNTIME_IMAGE=alpine:3.23

FROM --platform=$BUILDPLATFORM ${GO_BUILDER_IMAGE} AS builder

ARG TARGETOS
ARG TARGETARCH
ARG ALPINE_MIRROR=""
ARG GOPROXY=https://proxy.golang.org,direct

WORKDIR /src

RUN if [ -n "$ALPINE_MIRROR" ]; then \
      sed -i "s|https://dl-cdn.alpinelinux.org/alpine|$ALPINE_MIRROR|g" /etc/apk/repositories; \
    fi \
    && apk add --no-cache git bash

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    GOPROXY="$GOPROXY" go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    GOPROXY="$GOPROXY" go test ./... \
    && CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    GOPROXY="$GOPROXY" go build -trimpath -ldflags="-s -w" -o /out/wg-model-hub ./cmd/server \
    && CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    GOPROXY="$GOPROXY" go build -trimpath -ldflags="-s -w" -o /out/wg-model-hub-healthcheck ./cmd/healthcheck \
    && CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    GOPROXY="$GOPROXY" go build -trimpath -ldflags="-s -w" -o /out/wg-model-hub-migrate ./cmd/migrate

FROM ${ALPINE_RUNTIME_IMAGE} AS runtime

LABEL org.opencontainers.image.title="wg-model-hub" \
      org.opencontainers.image.source="https://github.com/wgdl666/wgModelHub"

ARG ALPINE_MIRROR=""
RUN if [ -n "$ALPINE_MIRROR" ]; then \
      sed -i "s|https://dl-cdn.alpinelinux.org/alpine|$ALPINE_MIRROR|g" /etc/apk/repositories; \
    fi \
    && apk add --no-cache --upgrade ca-certificates "openssl>=3.5.8-r0" \
    && addgroup -g 10001 app \
    && adduser -D -u 10001 -G app app

COPY --from=builder /out/wg-model-hub /usr/local/bin/wg-model-hub
COPY --from=builder /out/wg-model-hub-healthcheck /usr/local/bin/wg-model-hub-healthcheck

USER 10001:10001
EXPOSE 50053
ENTRYPOINT ["/usr/local/bin/wg-model-hub"]

FROM runtime AS migration

COPY --from=builder /out/wg-model-hub-migrate /usr/local/bin/wg-model-hub-migrate

ENTRYPOINT ["/usr/local/bin/wg-model-hub-migrate"]

FROM runtime AS production
