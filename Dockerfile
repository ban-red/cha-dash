# syntax=docker/dockerfile:1.27.1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e

# Cha Dash images: the hub, ghcr.io/ban-red/chadash (the default target),
# and the node, ghcr.io/ban-red/chadash-node (--target node).
#
# Stages:
#   1. web     builds the Vue SPA with Bun. It runs on the build machine's
#              platform, because the output is the same for every target.
#   2. hub     cross-compiles the Go binary with the SPA embedded (-tags webui),
#              and prepares the files the final image needs.
#   3. binary  the binary alone, for release downloads (--target binary).
#   4. node-build, node-binary, node   the node: the same Go toolchain without
#              the SPA, its binary alone (--target node-binary), and its image
#              (--target node), scratch with a non-root user.
#   5. final   scratch: the hub's binary, CA certificates and a non-root user.
#              Nothing else. The default target, so it stays last.
#
# Build (CI does this for linux/amd64, linux/arm64 and linux/arm/v7, with no
# emulation; arm/v7 is best-effort, see ADR-0016):
#   docker buildx build --platform linux/amd64,linux/arm64,linux/arm/v7 \
#     --build-arg VERSION=v0.1.0 --build-arg COMMIT=$(git rev-parse HEAD) .
#
# The release binaries come from the same steps, darwin included (Go
# cross-compiles; nothing from the image is needed):
#   docker buildx build --target binary --output type=local,dest=out \
#     --platform linux/amd64,linux/arm64,linux/arm/v7,darwin/amd64,darwin/arm64 \
#     --build-arg VERSION=v0.1.0 --build-arg COMMIT=$(git rev-parse HEAD) .
#
# The node the same way, with --target node (image) or --target node-binary
# (linux only: the node runs on a Docker host).
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
FROM --platform=$BUILDPLATFORM golang:1.27.3-trixie@sha256:c124b9ad3452f46061b5054607d26ea51730dae5372c5e469920063ba2bc6dcc AS hub

# The final image's filesystem. /data is staged on its own so that only it is
# owned by the runtime user; /tmp is for SQLite's temporary files.
# /pairing is where the hub writes the local node's pairing token: a volume
# shared with the node, group-readable so the node reads it through group
# 65532 (deploy/compose.yaml).
RUN mkdir -p /rootfs/etc/ssl/certs /rootfs/tmp /owned/data /owned/pairing \
 && echo 'chadash:x:65532:65532:Cha Dash:/nonexistent:/sbin/nologin' > /rootfs/etc/passwd \
 && echo 'chadash:x:65532:' > /rootfs/etc/group \
 && cp /etc/ssl/certs/ca-certificates.crt /rootfs/etc/ssl/certs/ \
 && chmod 1777 /rootfs/tmp \
 && chmod 0700 /owned/data \
 && chmod 0750 /owned/pairing

WORKDIR /src
# Modules first: cached until go.mod or go.sum change.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Go sources. A new top-level Go package directory needs a line here (CI
# checks this against `go list -deps ./cmd/chadash`).
COPY cmd cmd
COPY integrations integrations
COPY internal internal
COPY sim sim
COPY specs specs
COPY --from=web /src/internal/webui/dist internal/webui/dist

ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
ARG VERSION
ARG COMMIT
# GOARM comes from the platform's variant, not from Go's default for a
# cross-compiler (also 7), so the binary always matches the image's label.
# linux/arm/v7 is the only arm platform built (ADR-0016); any other arm
# variant stops the build rather than ship a binary that doesn't fit it.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    if [ "$TARGETARCH" = arm ]; then \
      case "$TARGETVARIANT" in \
        v7) export GOARM=7 ;; \
        *) echo "linux/arm/${TARGETVARIANT:-(no variant)} isn't built: only linux/arm/v7 (ADR-0016)" >&2; exit 1 ;; \
      esac; \
    fi \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -tags webui -trimpath \
      -ldflags "-s -w -X dash.cha.sh/internal/buildinfo.Version=${VERSION} -X dash.cha.sh/internal/buildinfo.Commit=${COMMIT}" \
      -o /out/chadash ./cmd/chadash

# ── 3. Binary only ────────────────────────────────────────────────────────────
# For .github/workflows/release.yml: exported with --output type=local, never
# pushed. The final image below stays the default target.
FROM scratch AS binary
COPY --from=hub /out/chadash /chadash

# ── 4. Node ───────────────────────────────────────────────────────────────────
# chadash-node: no SPA, and only the packages it imports (CI checks this
# against `go list -deps ./cmd/chadash-node`). It links the Docker client,
# which the hub never does (ADR-0006, ADR-0020).
FROM --platform=$BUILDPLATFORM golang:1.27.3-trixie@sha256:c124b9ad3452f46061b5054607d26ea51730dae5372c5e469920063ba2bc6dcc AS node-build

# /etc/chadash-node holds the identity and the policy. It's group-writable
# for 65532, so the node can run as uid 0 without capabilities (the socket's
# owner) with group 65532 added, as the compose file does, or as 65532.
RUN mkdir -p /rootfs/etc/ssl/certs /rootfs/tmp /owned/etc/chadash-node \
 && echo 'chadash:x:65532:65532:Cha Dash node:/nonexistent:/sbin/nologin' > /rootfs/etc/passwd \
 && echo 'chadash:x:65532:' > /rootfs/etc/group \
 && cp /etc/ssl/certs/ca-certificates.crt /rootfs/etc/ssl/certs/ \
 && chmod 1777 /rootfs/tmp \
 && chmod 0770 /owned/etc/chadash-node

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Go sources the node imports. A new top-level directory it imports needs a
# line here (CI checks this against `go list -deps ./cmd/chadash-node`).
COPY cmd cmd
COPY internal internal

ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
ARG VERSION
ARG COMMIT
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    if [ "$TARGETARCH" = arm ]; then \
      case "$TARGETVARIANT" in \
        v7) export GOARM=7 ;; \
        *) echo "linux/arm/${TARGETVARIANT:-(no variant)} isn't built: only linux/arm/v7 (ADR-0016)" >&2; exit 1 ;; \
      esac; \
    fi \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath \
      -ldflags "-s -w -X dash.cha.sh/internal/buildinfo.Version=${VERSION} -X dash.cha.sh/internal/buildinfo.Commit=${COMMIT}" \
      -o /out/chadash-node ./cmd/chadash-node

FROM scratch AS node-binary
COPY --from=node-build /out/chadash-node /chadash-node

FROM scratch AS node

COPY --from=node-build /rootfs/ /
COPY --from=node-build --chown=65532:65532 /owned/ /
COPY --from=node-build /out/chadash-node /chadash-node

# Non-root by default. To reach the Docker socket, the compose file runs it
# as uid 0 with every capability dropped (see deploy/README.md).
USER 65532:65532
VOLUME /etc/chadash-node

# The node listens on nothing: the check reads the state it writes to /tmp.
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --start-interval=2s --retries=3 \
  CMD ["/chadash-node", "healthcheck"]

ENTRYPOINT ["/chadash-node"]
CMD ["serve"]

ARG VERSION
ARG COMMIT
LABEL org.opencontainers.image.title="Cha Dash node" \
      org.opencontainers.image.description="Cha Dash's node: Docker control and owned stacks on one host, within its local policy" \
      org.opencontainers.image.source="https://github.com/ban-red/cha-dash" \
      org.opencontainers.image.url="https://github.com/ban-red/cha-dash" \
      org.opencontainers.image.documentation="https://github.com/ban-red/cha-dash/blob/main/deploy/README.md" \
      org.opencontainers.image.licenses="AGPL-3.0-or-later" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}"

# ── 5. Final image ────────────────────────────────────────────────────────────
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
