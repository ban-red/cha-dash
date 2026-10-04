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
- Every action is authorized centrally and audited.
- No unauthenticated network listeners.
- No third-party JavaScript in the Cha Dash origin.
- Releases will be signed and ship with an SBOM.

A full threat model will be published here before v1.0.

## Test data

Fixtures in this repo must never contain real credentials, domains or IPs. If you find any, please report it privately as described above.
