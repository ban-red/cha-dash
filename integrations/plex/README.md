# Plex (`plex`)

Reads a Plex Media Server: how many streams are playing, and how big the movie and show libraries are. The token can come from **Sign in with Plex** (plex.tv's PIN flow), so nobody has to dig an `X-Plex-Token` out of the web app.

**Tier:** silver. **Capabilities:** `media.sessions`, `media.library`. **Secret:** `token` (marked `"x-chadash-authorize": true`).

## Config

```json
{"url": "https://plex.lab.example", "tls": {"mode": "verify"}, "token": "${secret:instance/<id>/token}"}
```

| Key | Meaning |
|---|---|
| `url` | The server: behind a proxy (`https://plex.lab.example`), or direct (`https://<address>:32400`, whose `*.plex.direct` certificate verifies) |
| `tls` | The usual `{"mode", "pin"}`, for the server only |
| `token` | The owner account's token. Write-only, kept in the vault, sent as `X-Plex-Token` (never in the URL) |

This is the shape the Homepage importer writes.

## Sign in with Plex

The `token` property carries `"x-chadash-authorize": true`, so the settings form shows a **Sign in with Plex** button beside it. The flow is Plex's documented PIN flow ("Authenticating with Plex"), run by the hub through the generic `integration.Authorizer` and the owner-only routes `POST /api/integrations/authorize/plex` and `GET /api/integrations/authorize/plex/{id}` (see [`internal/runtime/authorize.go`](../../internal/runtime/authorize.go)):

1. **BeginAuth:** `POST https://plex.tv/api/v2/pins?strong=true` with `Accept: application/json`, `X-Plex-Product: Cha Dash`, `X-Plex-Version` and `X-Plex-Client-Identifier`. plex.tv answers a PIN `{id, code, expiresAt, …}`. The link is the Auth App, `https://app.plex.tv/auth#?clientID=<client id>&code=<code>&context%5Bdevice%5D%5Bproduct%5D=Cha%20Dash`. The PIN's id stays in the hub.
2. The browser opens the link; the owner signs in to Plex and approves Cha Dash.
3. **PollAuth:** `GET https://plex.tv/api/v2/pins/<id>?code=<code>` with the same headers, until `authToken` is set. A 404 means the PIN expired. The hub stops at `expiresAt` (30 minutes at most) either way.
4. The token goes back to the form, which fills the field. Nothing is stored until the owner saves.

**The client identifier** is stable per hub, as Plex asks: the runtime derives it from a random hub id kept in `settings` (`integrations/client_id`), hashed with the integration id, so Plex's "Authorized Devices" shows one Cha Dash however often you sign in, and no other source sees the same identifier.

**TLS:** the sign-in always verifies plex.tv's certificate against the system roots, whatever the form's `tls` says (that's for the server), and refuses a plex.tv URL that isn't https.

## What it reads

All `GET` on the server, with `Accept: application/json`:

| Endpoint | When | For |
|---|---|---|
| `/identity` | slow | version and machine identifier |
| `/library/sections` | slow | the libraries |
| `/library/sections/<key>/all?X-Plex-Container-Start=0&X-Plex-Container-Size=0` | slow, per movie or show section | `totalSize` |
| `/status/sessions` | every sync | what's playing (it needs the owner's token) |

**Poll plan:** every 30 s while watched, every 5 minutes otherwise, 30 s per sync. The slow reads run on the first sync, then every 30 minutes while watched.

## What it publishes

- **`plex:<instance id>/app`**, kind `app`, named after the instance, state `running`, health `ok`, with `attrs.type = "plex"`, `attrs.url`, `attrs.version`, `attrs.machine_identifier` (the natural key for the correlator), `attrs.movies` and `attrs.shows` (summed over the movie and show sections, as in the Demo Home), `attrs.libraries` (how many sections), a link, and `caps: ["media.sessions", "media.library"]`. `integration:<instance id> monitors` it, which is how the Now playing tile finds its repair.

| Metric | Unit | Scale | Band | Value |
|---|---|---|---|---|
| `streams` | count | 0–10 | 0–3 | `MediaContainer.size` of `/status/sessions`, playing or paused (nothing playing is 0) |

## Grants

A Plex token acts as the account: it can manage the server and the account, and Plex has no read-only tokens. Test reports one grant, `Plex account token`, access `admin`, and the hub warns about it.

## Repairs and hints

| What happened | Error | Field |
|---|---|---|
| 401 or 403 (signed out, revoked, or not the owner's account) | `auth` Repair: "Sign in with Plex again" | `token` |
| 404, or an answer that isn't JSON | `api_changed` Repair: check that the URL is a Plex Media Server | `url` |
| `/identity` without a machine identifier | `api_changed` Repair | `url` |

During sign-in: plex.tv unreachable or refusing is a `502` from the start route; a transient failure while polling keeps it pending (with the error), a refusal ends it as `failed`, an unknown PIN as `expired`.

## Limits

- Health is `ok` whenever the server answers; Plex has no health endpoint worth reading. An unreachable server is the framework's (series go stale, the app keeps its last state).
- No per-stream detail (who, what, transcode or not): the tile shows a count, and viewers' names are personal.
- Music and photo libraries aren't counted.
- Remote access, update availability and the server's own settings aren't read.

## Fixtures

Synthetic, in [`testdata/integrations/plex/`](../../testdata/integrations/plex/README.md).
