# ADR-0009 · Default port 3696

**Status:** accepted · **Date:** 2026-10-03 · **Roadmap:** [decision 9](../plan/04-roadmap.md#decisions)

## Context

Self-hosters run many services on one host, and the ports dashboards usually default to (3000, 7575, 8080) collide all the time. Nodes also need an endpoint on the hub to dial into.

## Decision

**Cha Dash listens on port 3696 by default**, for both the hub's HTTP interface and the node WebSocket endpoint at `/node`.

## Consequences

- One port to publish, proxy and remember: `http://<host>:3696`.
- The local node reaches the hub at `ws://chadash:3696/node` on the compose network. Remote nodes use the same path over TLS, usually through the reverse proxy.
- Docs, compose files, templates and reverse-proxy snippets all assume 3696. Anyone who needs another port changes the published side in compose (`8080:3696`), with no app setting involved.

## Alternatives considered

- **3000, 8080 or similar.** Memorable, but they collide with many other self-hosted apps.
- **A separate port for nodes.** A clearer split, but one more port to open, proxy and document.
- **A privileged port (80 or 443).** It needs extra capabilities in a non-root container, and it usually belongs to the reverse proxy.
