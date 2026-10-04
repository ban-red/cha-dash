# Homepage importer

Imports a [Homepage](https://gethomepage.dev) configuration. `services.yaml`, `docker.yaml` and `settings.yaml` go in; a Cha Dash board, integration instances and vaulted secrets come out, with a dry-run preview first.

- **`Build(Input)`** parses and maps. It's pure: no database, no network. It returns the plan and, separately, the secret values the plan moves.
- **`importer.Service`** ([`../`](../)) compares the plan with the hub (`Diff`, for the dry run) and applies it in one transaction (`Apply`).
- **`Register`** mounts `POST /api/import/homepage` (and `GET` for the last import's record: the board, the instances it created, links, discovery entries and board settings; tiles live on the board).

The acceptance test is [`testdata/importers/homepage/dogfood`](../../../testdata/importers/homepage/dogfood/README.md); its dry-run plan is pinned in [`expected-plan.json`](../../../testdata/importers/homepage/dogfood/expected-plan.json) (`go test ./internal/importer/homepage -run TestDogfoodImport -update` rewrites it).

## Mapping

### services.yaml

| Homepage | Cha Dash |
|---|---|
| Group | A layout section. The id is the group's slug (`Archive/Backup` → `archive-backup`). |
| Group order | The order Homepage renders: groups named in `settings.layout` first, in that order, then the rest in file order. A nested group gets its own section right after its parent. |
| Group as a list *or* a map; entries as a list *or* a map | Both accepted. |
| Entry | A tile. Its display name is `name:` if set (Homepage spreads it over the key), else the key. Entries are ordered by `weight`, defaulting to (index + 1) × 100, as Homepage does. |
| Entry with no name (`- "":`) | A chrome-less tile (`chromeless: true`, no header). |
| `href`, `icon`, `description` | Tile fields. `href` is the deep link; it's never polled. |
| `widget:` or `widgets:` | One integration instance per widget (table below). The first one is the entry's tile; extra widgets get their own tiles and instance names like `Arr (radarr)`. |
| Unknown widget type, or no type | **Unmapped** (`kind: widget`), with field *names* only. Its credentials aren't moved: they stay in the Homepage file. The entry still lands as a service tile. |
| `siteMonitor` | A target in the built-in `checks` instance (`kind: http`, or `tcp` for `host:port`), every 60 s. |
| No integration widget, but an `http(s)` href | Also a check, against the href, so every tile has a status (Linkding on the dogfood home). |
| `ping` | ICMP isn't a check kind. With an http(s) href and no widget, the href is checked instead (and `ping` is listed as ignored); otherwise unmapped (`kind: check`). |
| `server` + `container` | An explicit `runs_on` link from the entry's instance and tile to that container on that Docker instance. An unknown server, or one half of the pair, is unmapped (`kind: container_link`). |
| `showStats` | Tile option `show_stats`. |
| `statusStyle`, `target`, Kubernetes fields, anything else | Listed under `ignored`, with the entries that had them. |
| `{{HOMEPAGE_VAR_…}}` / `{{HOMEPAGE_FILE_…}}` | Not resolvable here. A secret field becomes `missing_secrets` on the instance (plus a non-Inbox `missing_secret` hint); a URL or identifier is unmapped (`kind: placeholder`). |

### Widgets → integrations

Integration IDs and config keys are the ones in [07 · v0.1 → Instance config keys](../../../docs/plan/07-v0.1.md#instance-config-keys). Every instance with a URL gets `url` and `tls`. Secret fields hold `${secret:instance/<instance id>/<field>}`.

| Homepage `type` | Integration | Config | Secrets (vault) | Tile | Tile options |
|---|---|---|---|---|---|
| `homeassistant` | `homeassistant` | `url` | `key` → `token` | `homeassistant` | `custom` |
| `frigate` | `frigate` | `url`, `username` | `password` (optional) | `cameras` | `enableRecentEvents` → `recent_events` |
| `opnsense` | `opnsense` | `url` | `username` → `key`, `password` → `secret` | `opnsense` | `wan` |
| `proxmox` | `proxmox` | `url`, `username` → `token_id` | `password` → `token_secret` | `service` | `node` |
| `proxmoxbackupserver` | `pbs` | `url`, `username` → `token_id` | `password` → `token_secret` | `service` | `datastore` |
| `traefik` | `traefik` | `url`, `username` | `password` (optional) | `traefik` | |
| `uptimekuma` | `uptimekuma` | `url`, `slug` | `key` (optional) | `uptimekuma` | |
| `unraid` | `unraid` | `url` | `key` → `api_key` | `service` | `pool1`–`pool4` |
| `plex` | `plex` | `url` | `key` → `token` | `plex` | |
| `sonarr`, `radarr` | same | `url` | `key` → `api_key` | `arr@<id>:<instance id>/app` | `enableQueue` → `queue` |
| `prowlarr`, `bazarr` | same | `url` | `key` → `api_key` | `service` | |
| `tautulli` | `tautulli` | `url` | `key` → `api_key` | `service` | `enableUser`, `showEpisodeNumber`, `expandOneStreamToTwoRows` |
| `qbittorrent` | `qbittorrent` | `url`, `username` | `password` | `downloads` | `enableLeechProgress` → `leech_progress` |
| `immich` | `immich` | `url` | `key` → `api_key` | `service` | |
| `mealie` | `mealie` | `url` | `key` → `token` | `service` | |
| `karakeep`, `hoarder` | `karakeep` | `url` | `key` → `api_key` | `service` | |
| `overseerr`, `jellyseerr` | `seerr` (Seerr is the successor of both, with the same API) | `url` | `key` → `api_key` | `service` | |
| `lidarr` | `lidarr` | `url` | `key` → `api_key` | `arr@lidarr:<instance id>/app` | `enableQueue` → `queue` |
| `transmission` | `transmission` | `url`, `username`, `rpcUrl` → `rpc_path` | `password` (optional) | `downloads` | |
| `nzbget` | `nzbget` | `url`, `username` | `password` (optional) | `downloads` | |
| `nextcloud` | `nextcloud` | `url`, `username` | `key` → `token`, or `password` (either; both optional) | `service` | |
| `audiobookshelf` | `audiobookshelf` | `url` | `key` → `api_key` | `service` | |
| `paperlessngx` | `paperlessngx` | `url` | `key` → `token` (a username and password are listed as ignored: the spec reads with a token) | `service` | |
| `speedtest` | `speedtesttracker` | `url` | `key` → `token` (Homepage's `version: 2`; `version` is dropped as for every widget) | `service` | |
| `calendar` | none: binds to instances | | | `calendar` | below |

Every widget's `fields` becomes the tile option `fields`; `version` is dropped (integrations negotiate it). Any other widget field is listed under `ignored`.

**Identifiers stay in config.** 07 lists `token_id` and `username` with the write-only fields; the importer treats them as identifiers. They're not secret, the dogfood fixture's 18 are exactly the passwords, keys and tokens, and vaulting them would make five `api@pam!homepage` token IDs look like one reused secret. The API should still mask them as write-only fields.

**TLS.** Homepage never verified certificates. An `https://` URL whose host is an IP, a bare name, or ends in `.lan`, `.local`, `.home.arpa`, `.internal` (and a few more) gets `{"mode": "tofu"}`, so a self-signed Proxmox certificate keeps working but a changed one is caught. Other names get `{"mode": "verify"}`, as does plain `http://`.

**The media calendar** (`type: calendar`) creates no instance. It binds to the instances its `integrations` name (by `service_group` and `service_name`, matching the entry's key or display name) through the `media.calendar` capability, keeping each source's `params` (`unmonitored`) and `color` as source options. `view`, `maxEvents`, `showTime`, `previousDays` and `timezone` become tile options; `firstDayInWeek` becomes the board setting `locale.first_day_of_week`. Sources other than Sonarr, Radarr and Lidarr, or names that don't resolve, are unmapped (`kind: calendar_source`).

### Tiles and the layout

The layout document follows `packages/layout` version 1 exactly (`version`, `columns: 12`, sections with `id`, optional `title`, `span`, `items`). Item positions are filled in with the engine's dense Z-reflow, so the stored document equals what the web app would serialize.

| Homepage | Layout |
|---|---|
| `layout.<group>.style: row` + `columns: N` | `span: N` (clamped to 1–12). `columns` is the section's internal column count, as the fixture README specifies. |
| No layout entry, or `style: column` | `span: 4`. |
| `header: false` | No `title` (an untitled section). |
| A group only in `settings.layout` | An empty section plus a `label_section` discovery entry: Homepage fills it from `homepage.*` container labels (Apps on the dogfood home). In Cha Dash the web app offers those containers as service tiles for it, through an Inbox discovery item (see `web/README.md`, "Label discovery"). |

Tile widget types come from `web/src/model/widgets.ts`. A catalog widget that shows one app is a singleton (`plex`, `cameras`, …) or bound to its instance's app (`arr@sonarr:<instance id>/app`, so each Sonarr gets its own queue tile); a second singleton of a kind falls back to the generic tile. Everything without a catalog widget is a **generic service tile**, `service@homepage:<section id>/<entry slug>`, sizes `small` (default) and `wide`. The web catalog doesn't have `service` yet. Each tile's default size shrinks to the closest supported size that fits its section (same height first, as `closestFit` does); if none fits, the section widens to the narrowest supported size and the summary says so.

Tile metadata the layout document can't carry (name, icon, href, description, options, the instance or check it shows, calendar sources) is in the plan's `tiles`. Apply stores it as the board's **tiles document**, on the same layout revision as the layout ([`internal/boards`](../../boards/tiles.go), served by `GET /api/boards/{id}/layout`), keyed by item id:

| Plan tile | Board tile |
|---|---|
| `item_id` | the key |
| `section`, `widget`, `size` | not stored: the layout already says |
| `name`, `icon`, `description`, `chromeless`, `options` | the same (empty ones left out) |
| `href` | the same when it's `http(s)://`; anything else is dropped, as the boards API refuses it |
| `instance`, `check`, `sources` | `bind: {instance, check, sources}` |
| `origin` | `origin: {source: "homepage", file, group, entry}` |

The layout and the tiles are compared separately (changes `layout` and `tiles`); either one differing writes one new revision with both. A re-import puts both back, so tile edits made on the board since are replaced, like layout edits. Imports from before migration `00004` kept the tiles in the import record; the migration moved them into the board in exactly this shape, so re-importing afterwards still changes nothing.

### docker.yaml

| Homepage | Cha Dash |
|---|---|
| `name: {host, port}` | A `docker` instance named after the server, `endpoint: tcp://host:port` (port defaults to 2375). |
| `name: {socket}` | `endpoint: unix://<path>`, plus a hint that the hub reads through a read-only socket proxy, not the socket. |
| `tls: {keyFile, caFile, certFile}` | Unmapped (`kind: docker_tls`): they're file paths on the Homepage host. |
| `swarm` | Ignored. |
| No host and no socket | Unmapped (`kind: docker_server`). |
| Every server | A `label_endpoint` discovery entry: containers there with `homepage.*` labels arrive through Docker discovery (wave 5). |

The import can't tell a raw Docker API from a filtering proxy: that takes the safe, read-only probe the Docker integration runs when it connects ([spike 4.4](../../../spikes/docker-proxy/README.md)). So each endpoint gets an `info` note, not an Inbox warning. A bare host name such as `dockerproxy` is called out as a compose service that only resolves on its compose network.

### settings.yaml

| Key | Handling |
|---|---|
| `title` | The board name (default "Homepage"); its slug is the board slug. |
| `layout` | Section order and column hints (map or list form; nested groups' layouts too). `icon`, `tab`, `initiallyCollapsed` and `useEqualHeights` inside it are listed as ignored. |
| `quicklaunch` | ⌘K is always on. `searchDescriptions` becomes the board setting `quicklaunch.search_descriptions`; `hideInternetSearch` and `hideVisitURL` are already how ⌘K behaves; anything else is ignored. |
| `instanceName` | A `self_host` hint (not Inbox): the hub probably runs on that host too. |
| `background`, `cardBlur`, `color`, `theme`, `statusStyle`, `useEqualHeights`, `headerStyle`, `disableCollapse`, `hideVersion`, `providers`, … | Listed under `ignored` with a reason. `providers` holds weather API keys; they're neither imported nor echoed. |

## Hints

| Code | Inbox | When |
|---|---|---|
| `reused_secret` | yes | Two or more moved secrets have the same value, found by an HMAC under a random per-import key. The hint names the instances and fields, never the value or the hash. |
| `plain_http_credentials` | yes | An instance with moved secrets has an `http://` URL. |
| `docker_endpoint` | no | Every Docker endpoint (see above). |
| `self_host` | no | `instanceName` is set. |
| `missing_secret` | no | A required secret was absent or a placeholder. |

Read-only PVE tokens, the Docker proxy tier, and Plex or Karakeep repairs are found by the integrations on a live run, not by the import.

## Identity and re-import

- **Deterministic IDs**, from the source path: instances `i_` + base32(SHA-256(`chadash/import/homepage`, file, group path, entry key, widget index))[:16]; Docker instances from the server name; the checks instance and the board (`b_…`) from fixed keys. Re-importing the same files produces the same ids, so `Apply` finds every row and changes nothing.
- **One Homepage import per hub.** A second import with different files updates the same board and instances.
- **Instance names** are display names, made unique per integration within the import (`Sonarr (Anime)`, then numbers). If an instance the import didn't create already has the name, the import's gets ` (imported)`.
- **Secrets** are named `instance/<instance id>/<field>`.

## Plan JSON

`POST /api/import/homepage?dry_run=1` answers:

```jsonc
{
  "plan": {
    "source": "homepage",
    "board": { "id": "b_…", "name": "Alpaca Homepage", "slug": "alpaca-homepage", "format_version": 1,
               "layout": { "version": 1, "columns": 12, "sections": [ … ] },
               "settings": { "locale": {…}, "quicklaunch": {…} } },
    "tiles":     [ { "item_id", "section", "widget", "size", "name", "icon", "href", "description",
                     "chromeless", "instance", "check", "sources", "options", "origin" } ],
    "instances": [ { "id", "integration", "name", "config", "secret_fields", "missing_secrets", "origin" } ],
    "secrets":   [ { "name": "instance/i_…/token", "instance": "i_…", "field": "token" } ],
    "secrets_moved": 18,
    "links":     [ { "type": "runs_on", "instance", "tile", "docker", "endpoint", "container", "origin" } ],
    "discovery": [ { "kind": "label_section" | "label_endpoint", "section", "docker", "endpoint", "message" } ],
    "ignored":   [ { "file", "key", "entries", "reason" } ],
    "summary":   { "sections", "tiles", "instances", "integrations", "checks", "links", "secrets_moved",
                   "hints", "inbox_hints", "unmapped", "discovery", "ignored", "notes" },
    "changes":   [ { "kind": "board" | "layout" | "tiles" | "instance" | "secret" | "record", "id", "name",
                     "action": "create" | "update" | "unchanged" | "stale" } ]
  },
  "hints":    [ { "code", "severity": "info" | "attention", "inbox", "message", "subjects": [ { "instance", "field", "origin" } ] } ],
  "unmapped": [ { "kind", "origin": { "file", "group", "entry" }, "type", "fields", "reason" } ]
}
```

No secret value appears anywhere in it: config holds refs, unmapped and ignored entries list field names, hints name instances.
