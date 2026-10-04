# Home files: export and import

A **Home file** is a whole hub's configuration as one YAML document: boards
with their layouts, integration instances, and the settings worth carrying.
`chadash export` and `GET /api/export` write one; `chadash import` and
`POST /api/import/chadash` read one back, after showing exactly what would
change.

The format is `apiVersion: chadash/v1alpha1`, `kind: Home`. It's unstable
until v1.0: a hub reads only its own version and refuses the rest.

## Example

```yaml
# Cha Dash Home file (chadash/v1alpha1): boards, integration instances and settings.
# Secrets are never in this file in plain text. Each one is a "!secret <name>"
# placeholder: a full backup restores them from the encrypted secrets_bundle
# (with its passphrase); otherwise enter them in Cha Dash after importing.
# Users, sessions, passkeys and the audit log are never exported.

apiVersion: chadash/v1alpha1
kind: Home
meta:
  exported_at: "2026-10-04T09:30:00Z"
  hub_version: 0.1.0
boards:
  - id: b_home
    name: Home
    slug: home
    position: 0
    layout:
      format_version: 1
      document:
        version: 1
        columns: 12
        sections:
          - id: media
            title: Media
            span: 8
            items:
              - {id: 'service@homepage:media/sonarr', type: service, size: small, supportedSizes: [small, wide], x: 0, y: 0}
    tiles:
      service@homepage:media/sonarr:
        name: Sonarr
        href: https://sonarr.home.arpa
        bind: {instance: i_sonarr}
        origin: {source: homepage, file: services.yaml, group: Media, entry: Sonarr}
instances:
  - id: i_sonarr
    integration: sonarr
    name: Sonarr
    config:
      url: https://sonarr.home.arpa
    tls: {mode: tofu, pin: 3f9a…}
    secrets:
      api_key: !secret instance/i_sonarr/api_key
  - id: i_traefik
    integration: traefik
    name: Traefik
    enabled: false
    config:
      url: http://traefik.home.arpa:8080
      username: admin
    secrets:
      password: !secret instance/i_traefik/password
settings:
  import/homepage:
    source: homepage
    board_id: b_home
    instances: [i_sonarr]
    links: []
    discovery: []
secrets_bundle:            # only in a full backup (--with-secrets)
  cipher: xchacha20poly1305
  kdf: {name: scrypt, log_n: 15, r: 8, p: 1, salt: mSwGjrBAT5zoZXEuwEtqpA==}
  nonce: 1iRlCmbwKRB7VdxwXVe3DVTgninzHhtA
  names:
    - instance/i_sonarr/api_key
    - instance/i_traefik/password
  ciphertext: |
    SDAjq18wUH1Bw6ZHq2slfw7QOrXiTwuH6mS3vSiCVu3K2UgxJUXqerXPTbsCJjQ92H6tr5eBxKnc
    …
```

## Fields

| Path | Required | Meaning |
|---|---|---|
| `apiVersion`, `kind` | yes | `chadash/v1alpha1`, `Home` |
| `meta.exported_at`, `meta.hub_version` | no | Where and when the file was made. Never compared on import |
| `boards[]` | | In display order |
| `.id`, `.name`, `.slug` | yes | `id`: letters, digits, `_ . -`; `name`: 1–64 characters; `slug`: lowercase letters, digits, dashes |
| `.position` | no (0) | The board's place in the list |
| `.layout` | no | Absent for a board never saved. `format_version` is the layout package's document format; `document` is the layout as the web app saved it, opaque to the hub (at most 1 MiB) |
| `.tiles` | no | The board's tiles document (`internal/boards/tiles.go`): what each layout item shows, keyed by item id (`name`, `icon`, `href`, `description`, `chromeless`, `bind {instance, check, resource, sources}`, `options`, `origin`). Written when the board has any. On import, no `tiles` key keeps the board's tiles; `tiles: {}` clears them |
| `instances[]` | | Sorted by integration, then name |
| `.id`, `.integration`, `.name` | yes | `name` is unique per integration |
| `.enabled` | no (true) | Written only when false |
| `.config` | no | The non-secret config: the keys in 07 · v0.1. Never a secret value or a `${secret:…}` reference |
| `.tls` | no | `{mode: verify\|pin\|tofu\|insecure, pin: <sha256>}`. `tls` inside `config` (the stored shape) is accepted on import too, but not both |
| `.secrets` | no | `<field>: !secret <vault name>` for every secret field that's set. `${secret:<vault name>}` is accepted on import. **Anything else is refused** |
| `settings` | no | Carried settings rows, by key: the import records (`import/<source>`), which keep what an import made beyond the board (explicit links, discovery hints, board settings, the instance ids for stale reports). Other keys are this hub's own state and stay |
| `secrets_bundle` | no | The encrypted values of a full backup (below) |

The decoder is strict: an unknown field, a repeated key, a YAML alias, a
custom tag (other than `!secret` under `secrets`), or a number JSON can't
hold is an error naming a line and a path. Errors never quote a value from
the file.

Never exported: users, sessions, passkeys, the audit log, layout history
(only the latest revision and its tiles), integration state
(`integration_state`, such as OPNsense's running totals), discovery
dismissals (`board_dismissals`: a label suggestion dismissed on one hub is
offered again after a restore), correlation decisions
(`correlation_decisions`: the correlator suggests again, and the owner
answers again), and settings that aren't carried (the onboarding wizard's
state, the client identity the hub signs in to Plex with).

## Secrets

**A Home file never holds a secret in plain text.** That's structural, not a
policy the encoder follows:

- **Placeholders are names.** An instance's secrets are a map of field to
  vault name; the document type has nowhere to put a value. The placeholder
  names the vault entry the instance needs: `instance/<instance id>/<field>`
  by the hub's convention, or whatever name a hand-written reference used.
- **Bundle values stay redacted until sealed.** While they're read from the
  vault they're `importer.Secret`, which prints `[redacted]` under every
  `fmt` verb, `slog` and JSON. Only the sealing step reveals them.
- **A leak guard.** Before writing, the exporter checks every string value in
  the document (never keys) against the stored secrets of 10 characters or
  more. A match means a value was also typed into a non-secret field, or
  sits in a carried setting (a token in a link's query string), so the file
  would leak it: the export is refused with `secret_in_plain_text`, naming
  the secret, never the value. Shorter secrets aren't checked: a
  dictionary-word password would match ordinary values by chance.
- **Plain values in the database stay behind.** A row written outside the
  API with a plain value in a secret field (or a reference nested where none
  belongs) is exported without that field, with a warning.
- **On import, plain values are refused**: anything under `secrets` that
  isn't a placeholder, a secret field inside `config`, and a reference
  anywhere in `config` or `settings`. The error names the path only.

### The encrypted bundle

`--with-secrets` (or `POST /api/export` with a passphrase) adds
`secrets_bundle`: every value the placeholders name, sealed together. It's
age's passphrase recipe simplified to one sealed blob, using the
XChaCha20-Poly1305 the vault already uses:

```
key        = scrypt(passphrase, "chadash.export/v1/scrypt" ‖ salt, N = 2^log_n, r = 8, p = 1, 32 bytes)
ciphertext = XChaCha20-Poly1305(key, nonce, {"<vault name>": "<value>", …} as JSON, aad = header)
```

- **Header as associated data.** The cipher, the KDF parameters, the salt
  (16 random bytes), the nonce (24 random bytes) and the sorted names are
  authenticated, so changing any of them fails exactly like a wrong
  passphrase. The names are in the clear on purpose: they're in the
  placeholders already, and a dry run without the passphrase can say what
  the bundle would restore.
- **Work factor.** `log_n` 15 (32 MiB, about a tenth of a second on a small
  ARM board), recorded in the file so it can rise later. A file asking for
  more than 2^18 (256 MiB) is refused before any work, and at most two
  derivations run at once.
- **Passphrases** need at least 12 characters (the hub's password rule) and
  at most 1024 bytes. A wrong one, or a changed bundle, is
  `wrong_passphrase`; the error never holds a value.

## Importing

Import is a plan, then one transaction. `Plan` (the dry run) and `Apply`
run the same comparison; `Apply` runs it inside the write transaction and
then writes only what differs.

- **Matching.** Boards and instances match by ID. A board whose ID the hub
  doesn't have also matches a board with the same slug that the file
  doesn't otherwise claim (so a fresh hub's default Home board takes the
  file's Home board instead of gaining a second one). The change then
  carries the file's ID as `file_id`, and the import records in `settings`
  are pointed at the hub's board.
- **Actions** (the importer's): `create`, `update`, `unchanged`, and
  `stale` for boards and instances the hub has that the file doesn't.
  **Nothing is ever deleted.** A layout and its tiles share a revision: a
  change to either appends one revision carrying both, so the hub's own
  history stays (and stays capped at 100 revisions).
- **Clashes.** An instance outside the file that holds a name keeps it; the
  imported one becomes `Name (imported)`. A slug another board holds gets
  `-2`, `-3`, …
- **Idempotent.** Objects equal as JSON values (key order and whitespace
  don't count) are left alone: importing an export of the same hub writes
  no row and bumps no timestamp. So does importing the same file twice.
- **Validation.** A known integration's config is checked against its
  schema; problems are warnings (the instance is imported and shows an error
  until fixed), except a secret field in `config` or a placeholder on a
  field that isn't secret, which refuse the file. An instance of an
  integration this hub doesn't include is imported with a warning. Tiles
  must pass the hub's own rules (`boards.ParseTiles`: known fields, http(s)
  links, sources with an instance and a capability) or the file is refused.
- **References.** Tiles name instances by ID (`bind.instance`,
  `bind.sources[].instance`). An import never renumbers instances (a name
  clash renames, the ID stays), so tiles need no remapping; a board matched
  by slug takes its tiles along.

### Secrets on import

For each placeholder, in order:

1. **The bundle**, if the passphrase opened it and its value differs from
   the vault's: the value is sealed into the vault, inside the import's
   transaction, under the placeholder's name (`source: bundle`).
2. **This hub's vault**, if it already holds that name (`source: vault`):
   re-importing into the same hub, or after the owner entered it.
3. **The field's current secret**, if the instance already has one that
   resolves, even under another name.
4. Otherwise the secret is **needed**: the instance is created (or kept)
   with that field unset and listed in `secrets_needed`. The runtime then
   shows the instance as `repair`, with a Repair whose `facts.field` is the
   missing field, so the Inbox's Reconnect opens the form on it; entering
   the value starts the instance and resolves the Repair. `in_bundle` says
   the locked bundle has it.

Secret fields the file doesn't mention keep their value, as in the
instances API. A restored value with an unchanged reference touches the
instance's `updated_at`, so a running hub restarts it with the new value.

### The plan

`POST /api/import/chadash?dry_run=1` answers `{"plan": …}`:

```json
{
  "format": "chadash/v1alpha1",
  "exported_at": "2026-10-04T09:30:00Z",
  "hub_version": "0.1.0",
  "changes": [
    {"kind": "board", "id": "b_home", "name": "Front page", "action": "update",
     "fields": [{"path": "/name", "op": "replace", "from": "Home", "to": "Front page"}]},
    {"kind": "layout", "id": "b_home", "name": "Front page", "action": "update",
     "fields": [
       {"path": "/document/sections/media/items/service@homepage:media~1sonarr/size", "op": "replace", "from": "small", "to": "wide"},
       {"path": "/document/sections/media/items/clock", "op": "add", "to": {"id": "clock", "type": "clock", "size": "small"}},
       {"path": "/document/sections", "op": "reorder", "from": ["media", "net"], "to": ["net", "media"]}]},
    {"kind": "tiles", "id": "b_home", "name": "Front page", "action": "update",
     "fields": [{"path": "/service@homepage:media~1radarr/name", "op": "replace", "from": "Radarr", "to": "Films"}]},
    {"kind": "layout", "id": "b_lab", "name": "Lab", "action": "create", "detail": "2 sections, 9 items"},
    {"kind": "instance", "id": "i_new", "name": "Proxy", "integration": "traefik", "action": "create",
     "fields": [{"path": "/config/url", "op": "add", "to": "https://proxy.home.arpa"}],
     "secrets": [{"field": "password", "secret": "instance/i_new/password", "action": "needed"}]},
    {"kind": "instance", "id": "i_sonarr", "name": "Sonarr", "integration": "sonarr", "action": "unchanged",
     "secrets": [{"field": "api_key", "secret": "instance/i_sonarr/api_key", "action": "unchanged", "source": "vault"}]},
    {"kind": "setting", "id": "import/homepage", "action": "unchanged"},
    {"kind": "instance", "id": "i_old", "name": "Old Radarr", "integration": "radarr", "action": "stale"}
  ],
  "secrets_needed": [
    {"instance": "i_new", "name": "Proxy", "integration": "traefik", "field": "password", "secret": "instance/i_new/password"}
  ],
  "bundle": {"present": false, "unlocked": false, "secrets": 0},
  "summary": {"create": 2, "update": 2, "unchanged": 2, "stale": 1, "secrets_restored": 0, "secrets_needed": 1},
  "warnings": [{"code": "unknown_integration", "kind": "instance", "id": "i_x", "message": "…"}]
}
```

- **Kinds**: `board`, `layout`, `tiles`, `instance`, `setting`. Stale objects
  come last.
- **Field changes** use JSON Patch's words (`add`, `replace`, `remove`) and
  JSON Pointer paths into the object, except that an array whose elements
  are objects with a unique `id` (or `item_id`) is addressed by that id, so
  moving a tile isn't a hundred index changes. When such an array's common
  elements change order, one `reorder` lists the ids before and after. An
  instance's paths are `/name`, `/enabled`, `/config/…` and `/tls/…`; a
  layout's are `/format_version` and `/document/…`; a tiles document's are
  `/<item id>/…`. At most 100 per object
  (`truncated: true` beyond). A created layout or tiles document has a
  `detail` ("2 sections, 9 items", "22 tiles") instead of its whole
  document; a created setting lists no fields.
- **Secrets** are reported per field, by name: `create`, `update`,
  `unchanged` or `needed`, with `source` `bundle` or `vault`. **A plan never
  holds a secret value.**
- **Warnings** codes: `unknown_integration`, `invalid_config`,
  `tiles_ignored` (tiles for a board with no layout to keep them with),
  `setting_ignored`, `no_bundle`.

Without `dry_run` the same call applies it and answers
`{"plan", "changed", "board_ids", "instance_ids"}`, where `plan` is what was
done.

## API

Every route needs the owner's session.

| Route | Body | Answer |
|---|---|---|
| `GET /api/export` | — | The Home file, placeholders only: `application/yaml`, `Content-Disposition: attachment; filename="chadash-home-<date>.yaml"` |
| `POST /api/export` | `{"passphrase"}` | The same with `secrets_bundle` (`…-with-secrets.yaml`). `422 weak_passphrase` under 12 characters |
| `POST /api/import/chadash?dry_run=1` | `{"yaml", "passphrase"?}` | `200 {"plan"}`. Writes nothing |
| `POST /api/import/chadash` | the same | `200 {"plan", "changed", "board_ids", "instance_ids"}`, in one transaction; the integrations restart right after |

Errors: `422 invalid_export` (the message names a line and a path),
`422 wrong_passphrase`, `422 bad_bundle`, `409 secret_in_plain_text` (an
export that would leak a value), `413 too_large` (a body over 6 MiB; a Home
file is at most 4 MiB). Logs record counts, never values or passphrases.

## CLI

```sh
chadash export [--out file] [--with-secrets]
chadash import file [--dry-run] [--json] [--skip-secrets]
```

Both read the data directory directly (`--data-dir` / `CHADASH_DATA_DIR`,
`--secret-key-file`), so they work with the hub running or stopped; in the
container, `docker exec -it chadash chadash export --out /data/home.yaml`.

- The passphrase comes from `CHADASH_EXPORT_PASSPHRASE`, else a prompt with
  echo off (asked twice on export). Without either, `export --with-secrets`
  fails, and `import` of a file with a bundle asks for one or for
  `--skip-secrets` (import without it; its secrets are then needed). A wrong
  passphrase fails before the data directory is touched.
- `--out` writes the file `0600`, atomically. Without the vault key, export
  still writes placeholders (the disaster-recovery case: the config survives
  a lost key); `--with-secrets` then fails.
- `import` may run on an empty data directory before the first boot: it
  creates the database and the vault key like the first boot would. A
  running hub starts imported instances within a minute.
- Output: the changes (unchanged objects are counted, not listed), field by
  field, the secrets still needed, and the totals. `--json` prints the API's
  shape. Exit 0 on success, 1 on failure, 2 on bad usage.

## Tile config

`tiles.go` is the only place that knows about tiles: `exportTiles` (what a
board's latest revision carries, when it's not empty) and `planTiles` (the
`tiles` change, validated with `boards.ParseTiles`). The board planner
writes one revision when the layout, the tiles or both change.
