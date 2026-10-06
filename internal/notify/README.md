# internal/notify

Notifications: Web Push to people's browsers, ntfy and webhooks for the household's own tools. Settings → Notifications in the web app drives it.

The service watches the `inbox` topic (a `livedoc.Doc`) and the action engine (`action.Options.OnTaskDone`), decides what's worth a message, and delivers each message to every channel its rule routes to, with retries, grouping, dedupe and rate limits. Everything it sends is in the delivery log.

## What notifies

Symptoms, not causes ([02 · Experience → Voice and copy](../../docs/plan/02-experience.md#voice-and-copy)). Five fixed rules; an admin can turn each off (except critical actions), route it to some channels, and choose whether resolutions are sent.

| Rule | When | Resolution notice by default |
|---|---|---|
| `incident.down` · Something is down | A problem opens at `down`, or an attention problem gets worse until it is. At once | yes |
| `incident.attention` · Needs attention for a while | An `attention` problem still open after the grace period (5 minutes by default, 1–240), and not in a fixing phase. Dropping back to `info` restarts the grace, so a flap never pages | no |
| `repair` · An integration needs reconnecting | A `repair` incident opens (not while it's in a fixing phase: the user is already on it) | no |
| `stack.stuck` · A stack is stuck | `internal/stacks` opens its `stack-stuck` incident: a deploy failed and so did its rollback | yes |
| `action.t4` · Critical actions | Every T4 task that ends, however it ended. Web Push reaches admins and the owner only. Always on | — |

**Never:** hints (an open `info` problem nothing is working on; `web/src/model/inbox.ts` `isHint`), anything at `info` (settling incidents included), the correlator's suggestions, or an incident that was already resolved when first seen. The security posture already decides a security finding's severity: under Relaxed every finding is `info`, a hint, so it never notifies; under Standard or Strict an `attention` finding follows the attention rule like any other problem.

**One message per incident per transition.** Each decision is a row in `notify_events`. A transition isn't sent again for the same incident within 24 hours unless it resolved in between, which is also what stops a restart from repeating every open problem. A resolution is only sent when the opening was.

**Grouped when many arrive at once.** Messages wait 10 seconds before going out. Three or more incident messages bound for the same destination become one ("5 new problems: 4 down, 1 integration to reconnect", with the first six titles), openings and resolutions apart. Critical actions and tests always go alone.

**Rate limits.** Each destination (a channel, or one device) gets 10 messages at once, then one every 2 minutes. Past that, messages are logged as `dropped`, not queued, so a storm can't become a backlog. Critical actions and tests are never limited.

## Channels

| Kind | Config | Secret (vault) |
|---|---|---|
| `webpush` | none; one built-in channel, `nc_webpush`, that can be turned off but not deleted | the hub's VAPID private key, `notify/vapid-key` |
| `ntfy` | `{server, topic}`: an http(s) URL without credentials, query or fragment; a topic of 1–64 `A–Z a–z 0–9 _ -` | `token`, optional: an ntfy access token, sent as `Authorization: Bearer` |
| `webhook` | `{url}`: an http(s) URL without credentials | `secret`, required, at least 16 characters: the HMAC key |

Secrets live in the vault as `notify/channel/<id>/<field>`. The API shows `{"set": bool}`; the audit log records which secrets changed and the host of a URL, never a value or a full URL (webhook URLs are often credentials themselves). Delivery errors are sentences the hub writes ("It answered 503 and asked to slow down."), never a transport error, a URL or a response body.

### Web Push

RFC 8030 delivery, RFC 8291 encryption (`aes128gcm`, one record, the `0x02` delimiter, no padding) and RFC 8292 VAPID, all with the standard library (`crypto/ecdh`, `crypto/hkdf`, `crypto/aes`, `crypto/cipher`, `crypto/ecdsa`). `TestEncryptRFC8291Vector` checks the encryption against the RFC's published example.

- **Keys.** The hub makes a P-256 VAPID key pair on first use and keeps the private key in the vault. `GET /api/notify/push/key` gives the public key (base64url, uncompressed point) for `pushManager.subscribe({applicationServerKey})`.
- **Requests.** `POST` to the endpoint with `Content-Encoding: aes128gcm`, `TTL: 86400`, `Urgency` (`high` for down and critical actions, `low` for resolutions, else `normal`), a `Topic` (32 base64url characters derived from the incident, so a newer message replaces an undelivered one), and `Authorization: vapid t=<JWT>, k=<public key>`. The JWT is ES256 over `aud` (the endpoint's origin), `exp` (12 hours) and `sub` (`CHADASH_PUBLIC_URL` when it's https, else `https://dash.cha.sh`). Plaintext stays under 3800 bytes.
- **Subscriptions** are per user and device (`notify_push_subscriptions`): any signed-in role manages their own. The endpoint must be an https URL naming a push service (no IP literals, `localhost` or single-label names), and the dialer only connects to public addresses. 404 and 410 mean the subscription expired: it's removed. The API never returns an endpoint or its keys, only its host and a fingerprint (the first 16 hex digits of its SHA-256) so a browser can find itself in the list.
- **Who hears what.** A device hears what its user's role sees of the inbox (`server.InboxView`): viewers and up get the message whole; household gets an incident about something on its boards as a title only, opening the front page; critical actions go to admins and the owner.
- **The service worker** (`web/sw/service-worker.js`) shows the notification and, on click, focuses an open tab and navigates it to the message's `path` (`/?incident=<id>` opens the Inbox on it), or opens one.

Service worker payload:

```jsonc
{ "v": 1, "kind": "incident.down", "severity": "down", "title": "PBS can't reach its datastore",
  "body": "… Down since 02:14 UTC.", "path": "/?incident=pbs.datastore", "tag": "pbs.datastore", "at": "2026-10-06T02:14:10Z" }
```

### ntfy

A `POST` of ntfy's JSON form to the server's root: `{topic, title, message, priority, tags, click}`. Priority follows severity: down 4, attention 3, anything else 2. Nothing is sent at 5 (urgent), which breaks through do-not-disturb: calm, never alarmist. Tags are `["chadash", <kind>]`. `click` is the hub's page for it, only when `CHADASH_PUBLIC_URL` is set.

### Webhook

A `POST` with `Content-Type: application/json`, the payload below, and these headers:

| Header | Value |
|---|---|
| `X-Chadash-Timestamp` | Unix seconds when it was sent |
| `X-Chadash-Signature` | `sha256=` + hex HMAC-SHA256, keyed with the channel's secret, over `<timestamp>.<body>`: the timestamp header's value, a dot, and the exact body bytes |
| `X-Chadash-Event` | the message's `kind` |
| `X-Chadash-Delivery` | the delivery's ID (`nd_…`), the same on every retry |

To verify: recompute the HMAC over the timestamp header, `.` and the raw body, compare in constant time, and refuse a timestamp more than a few minutes old. `notify.Sign` is the reference.

Payload, version 1 (`Message`):

```jsonc
{
  "version": 1,
  "id": "ne_…",                 // the event; a group is "ng_…"
  "kind": "incident.down",      // incident.down, incident.attention, repair, stack.stuck, incident.resolved, action.t4, group, test
  "severity": "down",           // down, attention or info
  "title": "PBS can't reach its datastore",
  "body": "PBS stopped answering. Down since 02:14 UTC.",
  "url": "https://dash.lab.example/?incident=pbs.datastore",  // only with CHADASH_PUBLIC_URL
  "at": "2026-10-06T02:14:10Z",
  "incident": { "id", "target", "scenario", "class", "severity", "status", "category"? },  // incident kinds
  "task": { "id", "action", "label", "target", "state", "actor", "audit"? },             // action.t4
  "items": [ { "id", "kind", "severity", "title", "incident"?, "url"? } ]               // group, at most 20
}
```

New fields may be added within version 1; a change that breaks a reader bumps `version`.

## Delivery

Every message to every destination is a `notify_deliveries` row: `pending`, then `sent`, `failed` or `dropped`. A failure worth retrying (a timeout, a refused connection, 408, 429, 5xx) waits 30 s, 2 min, 10 min, then 30 min, or longer when `Retry-After` asks (capped at an hour), for at most 5 attempts. A 4xx (other than 408 and 429) and a redirect are final. Pending rows survive a restart; one still pending after two days fails. Events and the log are kept 30 days. Tests (`POST …/test`) send once, at once, and are logged like any delivery.

**Outbound requests** go through one of two hardened clients: no proxy from the environment, redirects never followed, 5 s to connect, 15 s per request, 4 KiB of response read. The dialer checks the address it actually connects to, after DNS:

- **ntfy and webhooks** (`policyLAN`): the admin names the server and it's often on the LAN, so private and loopback addresses are allowed. Link-local (169.254.0.0/16, where cloud metadata answers, and fe80::/10), unspecified, multicast and broadcast are refused, at save time for literal addresses and at dial time for names.
- **Web Push** (`policyPublic`): anyone signed in can register an endpoint, so only public unicast addresses: no loopback, private, link-local or CGNAT (100.64.0.0/10) addresses.

## HTTP

| Route | Role | |
|---|---|---|
| `GET /api/notify/channels` | admin | `{"channels":[{id, kind, name, enabled, builtin, config, secrets:{field:{set}}, created_at, updated_at, last:{state, at, error}\|null, devices?}], "public_url": bool, "web_push": bool}` |
| `POST /api/notify/channels` | admin | `{kind, name, config, secrets, enabled?}` → `201` the channel. `422 invalid` with `fields`, `409 conflict` for a taken name |
| `PUT /api/notify/channels/{id}` | admin | `{name?, config?, secrets?, enabled?}`: in `secrets` a string sets, `null` or `""` clears, left out keeps |
| `DELETE /api/notify/channels/{id}` | admin | `204`; `409 builtin` for Web Push. Its log stays; rules forget it |
| `POST /api/notify/channels/{id}/test` | admin | `{ok, status_code, error, devices?}`. Web Push goes to your own devices: `409 no_devices` without one |
| `GET /api/notify/rules` | admin | `{"rules":[{id, label, description, enabled, resolved, grace_minutes?, channels, locked?, has_resolved, defaults, updated_at, updated_by?}]}`. `channels: null` is every enabled channel |
| `PUT /api/notify/rules/{id}` | admin | `{enabled?, resolved?, grace_minutes?, channels?}` → the rule. `409 locked` turning off `action.t4` |
| `GET /api/notify/deliveries?state=&limit=` | admin | `{"deliveries":[{id, channel_id, channel_name, channel_kind, device, kind, title, state, attempts, status_code, error, created_at, updated_at, next_at}]}`, newest first; `limit` 1–200 (50) |
| `GET /api/notify/push/key` | session | `{"public_key"}` |
| `GET /api/notify/push/subscriptions` | session | `{"subscriptions":[{id, label, host, fingerprint, created_at, last_ok_at, last_error}]}`: your own |
| `POST /api/notify/push/subscriptions` | session | `PushSubscription.toJSON()` plus `label?` (else guessed from the User-Agent) → `201` the device. At most 20 per user; an endpoint another user registered moves to you |
| `DELETE /api/notify/push/subscriptions/{id}` | session | `204`, your own only (`404` otherwise) |
| `POST /api/notify/push/unsubscribe` | session | `{endpoint}` → `204`, your own only |

Channel and rule changes, and tests, are audited as `settings.notify.channel.{create,update,delete,test}` and `settings.notify.rule.update` (`authz.Recorder`).

## Tables (`00011_notify`)

`notify_channels`, `notify_rules` (only what an admin changed), `notify_push_subscriptions`, `notify_events` (the decisions, for dedupe), `notify_deliveries` (the log).
