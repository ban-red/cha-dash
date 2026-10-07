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
  - It exposes capability verbs (`container.restart`), never a Docker API pass-through. Each verb names a permission in `/etc/chadash-node/policy.yaml`; the node refuses what the policy doesn't grant, and protected containers (Cha Dash itself, the node, and `docker.protect`) for every write. The hub can only ask. Exec and remove are off by default, and inspect leaves without environment values. That redaction covers the environment (`Config.Env`) only: a container's command line and labels can carry secrets too, and inspect returns them as they are.
  - **Deploying a stack is root on the node's host, by design.** A compose file can mount the host's disk, run privileged or join the host's namespaces, and stacks legitimately need bind mounts and capabilities, so an admin who can deploy controls that host. The node always refuses what would hand a stack its own keys: the Docker socket, `/`, the node's directories and volumes, the pairing volume, `volumes_from` another container, env files outside the stack's directory, `include` and `extends` from other files. The rest (privileged, host namespaces, devices, `cap_add`, host paths) is allowed but listed as root-equivalent in every plan and the deploy confirmation; the host owner can deny it with `stacks.deny` in the node's policy, or turn deploying off by removing `deploy` from `stacks.allow`. A deploy that would recreate or remove a protected container is refused.
  - The hub and the node run mutual TLS 1.3 inside the WebSocket, each known by an Ed25519 key the other pins ([ADR-0014](docs/adr/0014-node-transport.md)). No CA, no clock. A proxy in between sees ciphertext only. A rogue hub without the pinned key gets nothing, not even the pairing secret; a node whose key the registry doesn't know gets nothing either.
  - v0.2 pairs only the node on the hub's compose network, with a secret the hub writes into a volume only the two share. A stray container on the network has neither. The secret is single-use: the enrollment it admits spends it and the hub writes a fresh one, as it does on every revoke, at start once a node has paired, and when an admin regenerates it (`POST /api/nodes/pairing/regenerate`). Pairing is accepted only from private and loopback addresses unless `CHADASH_NODE_PAIRING_FROM` says otherwise, and it never replaces a live node of the same name: replacing one is an admin's revoke. Failed handshakes (bad secrets, unknown or revoked keys) are rate-limited per address and audited, handshakes in progress are capped per address and overall, and a hello is read under a 64 KiB limit.
  - Revoking a node is checked on every handshake and cuts a live session at once, and its key never works again: the revoked node can't pair its way back with a secret it read earlier. Key rotation is hygiene, not compromise recovery: after a hub compromise, re-pair every node.
  - Secrets (keys, the pairing secret) never appear in logs, errors, API responses or audit parameters. The node's private key is a 0600 file in its own volume; the hub's are sealed in the vault.
- Every action is authorized centrally and audited. Every HTTP route has a row in one table ([`internal/authz/routes.go`](internal/authz/routes.go)) naming the least role it admits; a route without one is refused, and CI fails on it. Every route that can change something either records what it changed in the audit log or is listed with the reason it doesn't ([`internal/authz/audited.go`](internal/authz/audited.go)); CI fails on one that does neither. Audit records hold names and IDs, never secret values.
- **The audit log is hash-chained but not anchored.** The chain shows edits and gaps made by anyone who doesn't recompute it, but someone who can write the SQLite file can cut events off the end, or rewrite events and every hash after them, and the chain will still verify. `chadash audit verify`, `GET /api/audit/verify` and the audit export (header `X-Chadash-Audit-Head`) give the chain's head (the last event's number and hash): record it somewhere the hub can't write, and check that a later chain still contains it. A keyed chain is future work.
- **Container logs can contain secrets** (tokens printed at start-up, connection strings). Reading them (`docker.container.logs`) is T0 but needs Operator or above, like every action marked as reading data back; household accounts run only actions marked for the household. Log lines live in the running task only and are never stored.
- **Live connections follow the session.** A WebSocket reads who it belongs to when it opens; signing out, being disabled, a role change and a handover of ownership close it at once, it rechecks its session every minute, and it reconnects after 12 hours whatever happens.
- **Household accounts see the Home Graph without the hub's own plumbing**: no chadash-node resources (key fingerprints, addresses, allowed verbs) and no IP or MAC addresses on anything else, on the live topic, `GET /api/graph` and the first page load alike.
- Five roles: owner, admin, operator, viewer and household. There is exactly one owner, who can't be demoted or disabled except by handing ownership to someone else. Users are disabled, never deleted, and disabling one ends their sessions at once.
- Destructive (T3) actions, handing over ownership and changing how people confirm them need a fresh re-verification on the session (step-up): a passkey, or the password for an account without one unless the owner requires passkeys. It lasts five minutes. Catastrophic (T4) actions are off unless the owner enables them per action, and always ask again.
- **Notifications** are the hub's only outbound requests that aren't to a source you configured ([`internal/notify`](internal/notify/README.md)). Notes:
  - Web Push goes to the browser vendors' push services (Google, Mozilla, Apple), which is inherent to Web Push. Payloads are end-to-end encrypted to the device (RFC 8291), so the push service sees only their size and timing. Because any signed-in user can register a device, endpoints must be https URLs naming a push service, and the hub only connects to public addresses for them. An endpoint belongs to the user who registered it: another account registering the same endpoint is refused (409) and the stored subscription is left as it was. Its owner can refresh its keys; to move a browser to another account, turn notifications off in the first one.
  - ntfy and webhooks go where an admin points them, often the LAN, so private addresses are allowed; link-local addresses (cloud metadata), multicast and unspecified ones are refused, checked on the address actually dialled after DNS. The checks also apply to the IPv4 address inside a NAT64 (`64:ff9b::/96`, `64:ff9b:1::/48`) or 6to4 (`2002::/16`) address, so one can't reach loopback or metadata through a translator; local-use NAT64 never counts as public. No request follows a redirect or uses an environment proxy, and each has short timeouts and a small response cap.
  - Webhook requests are signed with HMAC-SHA256 over a timestamp and the body, with a secret only the admin and the receiver hold. The ntfy token, the webhook secret and the hub's VAPID key live in the vault and never appear in responses, logs, delivery errors or audit parameters; the audit log keeps only a URL's host, since webhook URLs are often credentials too.
  - What a device is told follows its user's role: household devices hear only about what their boards show, by title, and critical actions reach admins and the owner only.
- **API tokens** are scoped, expiring credentials for scripts (`cdt_<id>_<secret>`, 256 random bits). Notes on their threat model:
  - Only the SHA-256 of a token is stored, compared in constant time. A slow hash like argon2id guards low-entropy passwords; a 256-bit random secret can't be guessed however fast the hash is. The secret is shown once and never appears in logs, errors, the audit log or later responses (the response that carries it is never compressed).
  - A token acts as its user and never above the user's current role, narrowed by its scopes. It works only in `Authorization: Bearer` on `/api/` routes, never as a cookie, so there's nothing for a cross-site request to ride on. Signing in, step-up, passkeys, users, tokens and settings refuse tokens: a stolen token can't mint another or raise itself.
  - A token can't step up, so T3 and T4 actions are out of its reach. Its T2 plans wait for a person, who approves them in a session with a fresh step-up, bound to the plan's hash. Revoking a token takes effect on its next request and withdraws what it left waiting.
  - Every token expires (a year at most) and shows when and from where it was last used. Bad tokens are rate-limited per client address.
  - Admins see and revoke everyone's tokens except the owner's, which only the owner sees. A token's actions name the user it acts for in the audit log (`on_behalf_of`).
- No unauthenticated network listeners.
- No third-party JavaScript in the Cha Dash origin.
- **Tile icons are fetched by the hub, under strict rules** ([`internal/icons`](internal/icons/icons.go)). The CSP keeps the browser from loading images from other origins, so the hub fetches each icon and serves it from its own origin. It's an outbound request the hub makes on the owner's behalf, so:
  - It fetches only what the owner configured: a tile's icon when it's an http(s) URL, else the favicon of a tile's link or an integration instance's URL (`/favicon.ico`, `/favicon.svg`, `/apple-touch-icon.png`, `/favicon.png`). No request can name a URL; `GET /api/icons/{id}` serves only what's stored.
  - http and https only, no credentials in the URL, no proxy. The address of every connection is checked when it's dialled, after DNS, so a rebinding answer can't slip one in: loopback, link-local (169.254.169.254 among it), "this network", unspecified, multicast, broadcast and the known cloud metadata addresses (`fd00:ec2::254`, `100.100.100.200`, `192.0.0.192`) are refused. Private LAN addresses are allowed, because that's where a homelab's services are. The IPv4 address inside a NAT64 or 6to4 address is checked by the same rules.
  - At most one redirect, to the same host. 5 seconds to connect, 10 in all. Certificates are verified, so a service with a self-signed certificate keeps its monogram.
  - The answer must be a 200 of at most 256 KiB whose bytes are a PNG, ICO, WebP or SVG, with a declared type that agrees (or a generic one). An SVG is parsed as XML and checked against an allowlist of elements and attributes, and refused rather than cleaned when anything falls outside it: scripts, event handlers, `foreignObject`, links, animation (`animate`, `set` and the rest), `feImage`, DOCTYPE and entity declarations, and `javascript:` even when written with character references. Every `href` and CSS `url()` must point inside the file (`#id`); the one exception is an embedded PNG, JPEG, GIF or WebP on `<image>`.
  - Icons are served with the checked type, `nosniff`, a private cache and `Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'; sandbox`, behind the session. The web shows them only in `<img>`, where scripts never run. Demo mode fetches nothing.
- Releases will be signed and ship with an SBOM.

A full threat model will be published here before v1.0.

## Test data

Fixtures in this repo must never contain real credentials, domains or IPs. If you find any, please report it privately as described above.
