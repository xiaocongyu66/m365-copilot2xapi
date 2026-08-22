ARG NODE_VERSION=22
ARG GO_VERSION=1.26
ARG ALPINE_VERSION=3.23

FROM --platform=$BUILDPLATFORM node:${NODE_VERSION}-alpine AS frontend-builder

WORKDIR /src/frontend
RUN corepack enable

COPY frontend/package.json frontend/pnpm-lock.yaml ./
RUN --mount=type=cache,id=m365copilot2xapi-pnpm,target=/pnpm/store \
    pnpm config set store-dir /pnpm/store && \
    pnpm fetch --frozen-lockfile

RUN --mount=type=cache,id=m365copilot2xapi-pnpm,target=/pnpm/store \
    pnpm config set store-dir /pnpm/store && \
    pnpm install --offline --frozen-lockfile

COPY frontend/index.html frontend/vite.config.ts frontend/tsconfig.json frontend/tsconfig.app.json frontend/tsconfig.node.json ./
COPY frontend/public ./public
COPY frontend/src ./src
RUN --mount=type=cache,id=m365copilot2xapi-tsc,target=/src/frontend/.cache,sharing=locked \
    pnpm build


FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS backend-builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src/backend
RUN apk add --no-cache ca-certificates git

COPY backend/go.mod backend/go.sum ./
RUN --mount=type=cache,id=m365copilot2xapi-go-mod,target=/go/pkg/mod,sharing=locked \
    go mod download

COPY backend/cmd ./cmd
COPY backend/internal ./internal
COPY backend/docs/docs.go ./docs/docs.go
RUN --mount=type=cache,id=m365copilot2xapi-go-mod,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=m365copilot2xapi-go-build,target=/root/.cache/go-build,sharing=locked \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/m365copilot2xapi ./cmd/m365copilot2xapi


FROM alpine:${ALPINE_VERSION}

ENV TZ=Asia/Shanghai \
    M365COPILOT2XAPI_CONFIG_SOURCE=/run/m365copilot2xapi/config.yaml

RUN apk add --no-cache ca-certificates su-exec tzdata && \
    addgroup -S -g 10001 m365copilot2xapi && \
    adduser -S -D -H -u 10001 -G m365copilot2xapi m365copilot2xapi && \
    mkdir -p /app/data /run/m365copilot2xapi /var/lib/m365copilot2xapi-quality-guard && \
    chown -R m365copilot2xapi:m365copilot2xapi \
      /app/data \
      /run/m365copilot2xapi \
      /var/lib/m365copilot2xapi-quality-guard && \
    chmod 0700 /var/lib/m365copilot2xapi-quality-guard

WORKDIR /app

COPY --from=backend-builder --chmod=0755 /out/m365copilot2xapi /app/m365copilot2xapi
COPY --from=frontend-builder /src/frontend/dist /app/frontend/dist
COPY VERSION /app/VERSION
COPY --chmod=0755 docker/entrypoint.sh /usr/local/bin/m365copilot2xapi-entrypoint

EXPOSE 8000

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8000/healthz >/dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/m365copilot2xapi-entrypoint"]
CMD ["/app/m365copilot2xapi", "--config", "/app/config.yaml", "--listen", "0.0.0.0:8000"]
