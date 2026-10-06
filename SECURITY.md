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
- **Notifications** are the hub's only outbound requests that aren't to a source you configured ([`internal/notify`](internal/notify/README.md)). Notes:
  - Web Push goes to the browser vendors' push services (Google, Mozilla, Apple), which is inherent to Web Push. Payloads are end-to-end encrypted to the device (RFC 8291), so the push service sees only their size and timing. Because any signed-in user can register a device, endpoints must be https URLs naming a push service, and the hub only connects to public addresses for them.
  - ntfy and webhooks go where an admin points them, often the LAN, so private addresses are allowed; link-local addresses (cloud metadata), multicast and unspecified ones are refused, checked on the address actually dialled after DNS. No request follows a redirect or uses an environment proxy, and each has short timeouts and a small response cap.
  - Webhook requests are signed with HMAC-SHA256 over a timestamp and the body, with a secret only the admin and the receiver hold. The ntfy token, the webhook secret and the hub's VAPID key live in the vault and never appear in responses, logs, delivery errors or audit parameters; the audit log keeps only a URL's host, since webhook URLs are often credentials too.
  - What a device is told follows its user's role: household devices hear only about what their boards show, by title, and critical actions reach admins and the owner only.
- **API tokens** are scoped, expiring credentials for scripts (`cdt_<id>_<secret>`, 256 random bits). Notes on their threat model:
  - Only the SHA-256 of a token is stored, compared in constant time. A slow hash like argon2id guards low-entropy passwords; a 256-bit random secret can't be guessed however fast the hash is. The secret is shown once and never appears in logs, errors, the audit log or later responses (the response that carries it is never compressed).
  - A token acts as its user and never above the user's current role, narrowed by its scopes. It works only in `Authorization: Bearer` on `/api/` routes, never as a cookie, so there's nothing for a cross-site request to ride on. Signing in, step-up, passkeys, users, tokens and settings refuse tokens: a stolen token can't mint another or raise itself.
  - A token can't step up, so T3 and T4 actions are out of its reach. Its T2 plans wait for a person, who approves them in a session with a fresh step-up, bound to the plan's hash. Revoking a token takes effect on its next request and withdraws what it left waiting.
  - Every token expires (a year at most) and shows when and from where it was last used. Bad tokens are rate-limited per client address.
- No unauthenticated network listeners.
- No third-party JavaScript in the Cha Dash origin.
- **Tile icons are fetched by the hub, under strict rules** ([`internal/icons`](internal/icons/icons.go)). The CSP keeps the browser from loading images from other origins, so the hub fetches each icon and serves it from its own origin. It's an outbound request the hub makes on the owner's behalf, so:
  - It fetches only what the owner configured: a tile's icon when it's an http(s) URL, else the favicon of a tile's link or an integration instance's URL (`/favicon.ico`, `/favicon.svg`, `/apple-touch-icon.png`, `/favicon.png`). No request can name a URL; `GET /api/icons/{id}` serves only what's stored.
  - http and https only, no credentials in the URL, no proxy. The address of every connection is checked when it's dialled, after DNS, so a rebinding answer can't slip one in: loopback, link-local (169.254.169.254 among it), "this network", unspecified, multicast, broadcast and the known cloud metadata addresses (`fd00:ec2::254`, `100.100.100.200`, `192.0.0.192`) are refused. Private LAN addresses are allowed, because that's where a homelab's services are.
  - At most one redirect, to the same host. 5 seconds to connect, 10 in all. Certificates are verified, so a service with a self-signed certificate keeps its monogram.
  - The answer must be a 200 of at most 256 KiB whose bytes are a PNG, ICO, WebP or SVG, with a declared type that agrees (or a generic one). An SVG with a script, an event handler, `foreignObject`, a DOCTYPE or entity, `javascript:`, or an external reference is refused rather than cleaned.
  - Icons are served with the checked type, `nosniff`, a private cache and `Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'; sandbox`, behind the session. The web shows them only in `<img>`, where scripts never run. Demo mode fetches nothing.
- Releases will be signed and ship with an SBOM.

A full threat model will be published here before v1.0.

## Test data

Fixtures in this repo must never contain real credentials, domains or IPs. If you find any, please report it privately as described above.
