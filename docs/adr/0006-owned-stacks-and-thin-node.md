# ADR-0006 · Cha Dash owns stacks; a thin node does the writing

**Status:** accepted · **Date:** 2026-10-03 · **Roadmap:** [decision 6](../plan/04-roadmap.md#decisions)

## Context

Read-only dashboards get abandoned, and "deploy or adjust a stack" is a daily homelab job. Doing it well means Cha Dash has to own compose files, not just show containers. But anything that writes to Docker is effectively root on that host, and the hub is a web app that holds every credential in the house. See [03 · Architecture → Stacks](../plan/03-architecture.md#stacks-cha-dash-owns-them-) and [The node](../plan/03-architecture.md#the-node-chadash-node).

## Decision

**Cha Dash owns stacks.**
- Compose files live **on disk, on the host they run on**, at `<stacks root>/<stack>/compose.yaml` (default root `/opt/stacks`), never hidden in a database.
- Cha Dash metadata sits inside the file under `x-chadash:`, so it stays valid compose.
- **Each stacks root is a git repo.** Every deploy is a commit, so history, diff and rollback are just git.
- Secrets stay as `${secret:name}` references. They're resolved in memory at deploy time and never committed.
- **Adopt and import are first-class.** Adopt a running project in place, or move it into the root under the same project name, so its volumes and networks survive. Importers for other stack layouts follow.

**A thin `chadash-node` on each Docker server does all the writing.**
- It's the **only** component that changes Docker. The hub never mounts a writable `docker.sock`, so it stays unprivileged.
- It dials **out** to the hub, so no inbound ports are needed.
- It exposes capability verbs (`container.restart`, `stack.deploy`), not a Docker API pass-through, and enforces a **local policy file** the hub can't override.

## Consequences

- Two binaries and two images.
- Rollout: in v0.2 a local node joins the hub's own compose file and replaces the socket proxy; in v0.3 remote nodes run on every Docker server.
- There's one code path for Docker control from the first day of control.
- Nodes must run happily inside LXCs, like pvedocker.
- Owning stacks means we can break production. Plans, health gates and automatic rollback are mandatory ([ADR-0020](0020-node-deploys-with-compose-sdk-and-go-git.md)).
- Unraid-managed containers are not owned stacks ([ADR-0010](0010-unraid-stays-unraid.md)).
- Node transport and identity are still open (ADR-0014).

## Alternatives considered

- **The hub mounts the Docker socket.** Simplest, but a bug in the web layer becomes root on the host.
- **Stacks stored in the hub's database.** Easy to build, but opaque, and hard to leave.
- **A filtering proxy in front of the raw Docker API for writes.** Path filtering is fragile: missed endpoints and path tricks are a recurring vulnerability class.
- **Agentless control over SSH.** No extra container, but it needs inbound access and long-lived keys held by the hub.
- **Show containers, don't own stacks.** Safer, but it leaves deploys, history and rollback to other tools.
