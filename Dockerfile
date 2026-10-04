# syntax=docker/dockerfile:1.27.1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e

# Cha Dash hub image: ghcr.io/ban-red/chadash
#
# Three stages:
#   1. web    builds the Vue SPA with Bun. It runs on the build machine's
#             platform, because the output is the same for every target.
#   2. hub    cross-compiles the Go binary with the SPA embedded (-tags webui),
#             and prepares the files the final image needs.
#   3. final  scratch: the binary, CA certificates and a non-root user. Nothing else.
#
# Build (CI does this for linux/amd64 and linux/arm64, with no emulation):
#   docker buildx build --platform linux/amd64,linux/arm64 \
#     --build-arg VERSION=v0.1.0 --build-arg COMMIT=$(git rev-parse --short HEAD) .
#
# Base images are pinned by tag and digest. Bump both together, and keep Bun in
# step with BUN_VERSION in .github/workflows/ci.yml.

ARG VERSION=dev
ARG COMMIT=dev

# ── 1. SPA ────────────────────────────────────────────────────────────────────
FROM --platform=$BUILDPLATFORM oven/bun:1.4.2-slim@sha256:cb3bbbb08e13a4a2ff400f24c7a2a1d5efa83f6ef8544d52d95a519631e2fc61 AS web
WORKDIR /src
ENV BUN_INSTALL_CACHE_DIR=/cache/bun

# Lockfile and workspace manifests first, so the install layer survives source edits.
COPY --parents package.json bun.lock packages/*/package.json design/tokens/package.json web/package.json ./
RUN --mount=type=cache,target=/cache/bun \
    bun install --frozen-lockfile

# Only what the SPA build reads. Vite writes to internal/webui/dist/.
COPY tsconfig.base.json ./
COPY packages packages
COPY design/tokens design/tokens
COPY web web
RUN bun run build

# ── 2. Hub binary ─────────────────────────────────────────────────────────────
FROM --platform=$BUILDPLATFORM golang:1.27.1-trixie@sha256:3b77fc618ec235a1ab412de7737f120dd507c57e8d87de4cbb7994fb94275ed5 AS hub

# The final image's filesystem. /data is staged on its own so that only it is
# owned by the runtime user; /tmp is for SQLite's temporary files.
RUN mkdir -p /rootfs/etc/ssl/certs /rootfs/tmp /owned/data \
 && echo 'chadash:x:65532:65532:Cha Dash:/nonexistent:/sbin/nologin' > /rootfs/etc/passwd \
 && echo 'chadash:x:65532:' > /rootfs/etc/group \
 && cp /etc/ssl/certs/ca-certificates.crt /rootfs/etc/ssl/certs/ \
 && chmod 1777 /rootfs/tmp \
 && chmod 0700 /owned/data

WORKDIR /src
# Modules first: cached until go.mod or go.sum change.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Go sources. A new top-level Go package directory needs a line here.
COPY cmd cmd
COPY internal internal
COPY sim sim
COPY --from=web /src/internal/webui/dist internal/webui/dist

ARG TARGETOS
ARG TARGETARCH
ARG VERSION
ARG COMMIT
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -tags webui -trimpath \
      -ldflags "-s -w -X dash.cha.sh/internal/buildinfo.Version=${VERSION} -X dash.cha.sh/internal/buildinfo.Commit=${COMMIT}" \
      -o /out/chadash ./cmd/chadash

# ── 3. Final image ────────────────────────────────────────────────────────────
FROM scratch

COPY --from=hub /rootfs/ /
COPY --from=hub --chown=65532:65532 /owned/ /
COPY --from=hub /out/chadash /chadash

USER 65532:65532
VOLUME /data
EXPOSE 3696

# Scratch has no curl: the binary checks its own /healthz. Exit 0 healthy, 1 not.
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --start-interval=2s --retries=3 \
  CMD ["/chadash", "healthcheck"]

ENTRYPOINT ["/chadash"]
CMD ["serve"]

ARG VERSION
ARG COMMIT
LABEL org.opencontainers.image.title="Cha Dash" \
      org.opencontainers.image.description="The front page of your home: a self-hosted homelab dashboard" \
      org.opencontainers.image.source="https://github.com/ban-red/cha-dash" \
      org.opencontainers.image.url="https://github.com/ban-red/cha-dash" \
      org.opencontainers.image.documentation="https://github.com/ban-red/cha-dash/blob/main/deploy/README.md" \
      org.opencontainers.image.licenses="AGPL-3.0-or-later" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}"
