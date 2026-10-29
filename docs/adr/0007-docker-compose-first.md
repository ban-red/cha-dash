# ADR-0007 · Docker Compose is the main install method

**Status:** accepted · **Date:** 2026-10-03 · **Roadmap:** [decision 7](../plan/04-roadmap.md#decisions)

## Context

Most homelabs already run Docker, and most self-hosters manage it with compose files. Cha Dash also needs companions (a socket proxy now, a node later), which a compose file expresses cleanly. The Pact promises "one `docker compose up -d`" with no required configuration. See [03 · Architecture → Deployment](../plan/03-architecture.md#deployment-docker-compose-first-).

## Decision

**`docker compose up -d` is the main way to self-host Cha Dash, and every install doc starts with a `compose.yaml`.**
- **Zero required environment variables.** First boot generates the vault key and prints a setup token to the logs. *Replaced by [ADR-0028](0028-first-run-without-a-setup-token.md): no setup token; the first visitor creates the owner.*
- One persistent volume: `/data`.
- An optional, pre-tuned, read-only socket-proxy sidecar lets v0.1 see containers on the host.
- **Adding a node is a compose snippet too.** The hub generates it with a single-use join token filled in.
- The shipped compose files live in `deploy/`.

Secondary channels wrap the same images: Unraid Community Apps templates and binaries with systemd (v0.1), a TrueNAS community app (v0.3), and Helm (v1.0).

## Consequences

- Images are pinned, signed and multi-arch (amd64 and arm64; armv7 best-effort, pending ADR-0016), non-root, built from scratch or distroless.
- A scratch image has no curl, so the binary carries its own `healthcheck` subcommand.
- `CHADASH_SECRET_KEY_FILE` moves the vault key out of `/data` for anyone who wants that. Nobody has to.
- v0.1's exit test is timed from `docker compose up -d`: a populated, live page in under 3 minutes.
- Kubernetes users are served later and less directly. Enterprise and Kubernetes fleet management is a non-goal.

## Alternatives considered

- **A single `docker run` line.** Shorter to paste, but sidecars and nodes would need a second, different set of instructions.
- **Native packages or binaries first.** Lighter, but most of the audience already lives in Docker, and upgrades are harder to make uniform.
- **Kubernetes or Helm first.** Not where homelabs are.
