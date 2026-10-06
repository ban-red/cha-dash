# Security Policy

Cha Dash is built to hold credentials for, and eventually control, the machines in your home. We take that seriously.

## Reporting a vulnerability

**Please don't open a public issue for security problems.**

Report them privately through GitHub's **[private vulnerability reporting](https://github.com/ban-red/cha-dash/security/advisories/new)** (Security tab → "Report a vulnerability"). Please include:

- what's affected (hub, node, a specific integration) and which version or commit
- steps to reproduce, or a proof of concept
- what you think the impact is

You'll get an acknowledgement within a few days. Once a fix is ready, we'll coordinate a disclosure date with you and credit you in the advisory unless you'd rather stay anonymous.

## Supported versions

Cha Dash is in **beta** (`0.1.0-beta.N`). Security fixes go to the latest beta; there are no maintained older lines before 1.0, so upgrade to the newest tag.

Release images and binaries are signed with cosign (keyless, GitHub OIDC) and published with an SBOM and build provenance. [docs/beta.md](docs/beta.md) shows how to verify them.

## Design commitments

These are tracked as requirements in [docs/plan/03-architecture.md](docs/plan/03-architecture.md#auth-and-security):

- Secrets never reach the browser. They're encrypted at rest and redacted in exports and diagnostics.
- The hub never has write access to Docker. Only nodes do, and only within each host's local policy.
- **The node** (`chadash-node`) holds a host's Docker socket, which is root on that host. Notes on its threat model:
  - It exposes capability verbs (`container.restart`), never a Docker API pass-through. Each verb names a permission in `/etc/chadash-node/policy.yaml`; the node refuses what the policy doesn't grant, and protected containers (Cha Dash itself, the node, and `docker.protect`) for every write. The hub can only ask. Exec and remove are off by default, and inspect leaves without environment values.
  - The hub and the node run mutual TLS 1.3 inside the WebSocket, each known by an Ed25519 key the other pins ([ADR-0014](docs/adr/0014-node-transport.md)). No CA, no clock. A proxy in between sees ciphertext only. A rogue hub without the pinned key gets nothing, not even the pairing secret; a node whose key the registry doesn't know gets nothing either.
  - v0.2 pairs only the node on the hub's compose network, with a secret the hub writes into a volume only the two share. A stray container on the network has neither. Failed pairings are rate-limited per address and audited.
  - Revoking a node is checked on every handshake and cuts a live session at once. Key rotation is hygiene, not compromise recovery: after a hub compromise, re-pair every node.
  - Secrets (keys, the pairing secret) never appear in logs, errors, API responses or audit parameters. The node's private key is a 0600 file in its own volume; the hub's are sealed in the vault.
- Every action is authorized centrally and audited. Every HTTP route has a row in one table ([`internal/authz/routes.go`](internal/authz/routes.go)) naming the least role it admits; a route without one is refused, and CI fails on it.
- Five roles: owner, admin, operator, viewer and household. There is exactly one owner, who can't be demoted or disabled except by handing ownership to someone else. Users are disabled, never deleted, and disabling one ends their sessions at once.
- Destructive (T3) actions, handing over ownership and changing how people confirm them need a fresh re-verification on the session (step-up): a passkey, or the password for an account without one unless the owner requires passkeys. It lasts five minutes. Catastrophic (T4) actions are off unless the owner enables them per action, and always ask again.
- No unauthenticated network listeners.
- No third-party JavaScript in the Cha Dash origin.
- Releases will be signed and ship with an SBOM.

A full threat model will be published here before v1.0.

## Test data

Fixtures in this repo must never contain real credentials, domains or IPs. If you find any, please report it privately as described above.
