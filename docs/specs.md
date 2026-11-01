# Writing an integration spec

A spec is a YAML file that teaches Cha Dash to read a service's HTTP API: what to ask for, what to publish, and, if you want, a few actions. The hub runs it with its spec engine. Nine integrations ship as specs ([`specs/`](../specs/README.md)); [`seerr.yaml`](../specs/seerr.yaml) is the fullest example.

The format is `chadash/v1alpha1`, public since v0.3 ([ADR-0025](adr/0025-spec-format-public-as-v1alpha1.md)). "Alpha" means it can still change, but only by a new `apiVersion` with a migration; a hub loads the old version for at least one minor release after.

## Check it as you write

- **The schema.** Every hub serves the format's JSON Schema at `/api/specs/schema/v1alpha1.json`. Point your editor at it, for example with a first line `# yaml-language-server: $schema=https://<your hub>/api/specs/schema/v1alpha1.json`.
- **`chadash spec lint <spec.yaml>`** checks the schema and the rules the hub applies when it loads a spec: patterns, templates, where secrets may go, expressions that compile.
- **`chadash spec test <spec.yaml> <fixtures/>`** runs the spec against recorded answers on a local fake upstream, prints the resources and metrics it publishes, and checks what every spec must publish. Neither command needs a running hub.

Run them from the hub's image: `docker run --rm -v "$PWD:/w" -w /w ghcr.io/ban-red/chadash spec lint my.yaml`.

### Fixtures

A fixtures folder answers the spec's requests by name: `<request>.json` is a 200 with that body, and a request without a file gets a 404. A `fixtures.yaml` says more ([example](../specs/testdata/linkding/fixtures.yaml)):

```yaml
config:  { inbox_warn: 10 }    # non-secret config; url is the fake upstream
secrets: { token: fake-token } # each defaults to "fake-<name>"
answers:
  server: health.json          # a file, answered with a 200
  stats: { status: 401 }       # a status, with an optional file
```

Record the answers from your own instance, then take out anything personal: names, titles, addresses, tokens. The community repository refuses fixtures with real data.

`spec test` passes when the connection test passes, every request that isn't `optional` has an answer, and the spec publishes `<id>:<instance>/app`: kind `app`, named after the instance, state `running`, with `attrs.type` set to the spec's ID, one link to the configured URL, and the instance monitoring it.

## Install it

Settings → Integrations → Installed specs (the owner only): paste or choose the file and press **Check**. Cha Dash lists every request it makes, with the secrets each one sends, and every action it adds, with the action's method, path and tier. Requests sent as POST are listed apart, with their body: a POST can change things whatever it's called. **Install** asks you to confirm it's you. The spec then shows on the Add list, marked installed, and its reads start with its first instance.

**Its actions stay off** until you press **Turn actions on**, which asks you to confirm it's you again, and so do its POST requests: until then its instances say why they don't read. Replacing a spec with a version whose actions, POST requests, source headers or config fields differ turns them off again. A secret the app sends back in an answer is replaced by `[redacted]` before any expression sees it. Expressions can't be stopped mid-run, so a spec whose expressions loop over a large answer can keep a core busy: install specs you've read. A spec with instances can't be removed until they go, and an ID one of Cha Dash's own integrations uses is refused.

Installed specs live in the hub's database, so a backup carries them.

## The format

```yaml
apiVersion: chadash/v1alpha1
kind: Integration
meta: { id: seerr, name: Seerr, aliases: [Overseerr, Jellyseerr], icon: seerr, version: 0.1.0, tier: bronze, category: media, capabilities: [media.requests] }
config:                           # url and tls are always added; types: string, url, secret, integer, number, boolean
  api_key: { type: secret, title: API key, required: true }
source:
  headers: { X-Api-Key: "{{ secret.api_key }}" }   # templates: config.x, secret.x, instance.id, instance.name
  timeout: 10s                    # per request, at most 30s
every: 60s                        # at least 10s
requests:                         # run in order, GET (or POST with body)
  counts:
    path: /api/v1/request/count   # under the instance URL's path; no secrets here
    query: { take: "1" }          # optional; secrets allowed (kept out of errors)
    extract: { pending: pending } # name → gjson path; missing is null
    optional: false               # true: a failure leaves the values null instead of failing the sync
version: status.version           # an expression over {config, instance, <request>: {<name>: value}}
resources:
  - id: app                       # the resource is <spec id>:<instance id>/app
    kind: app
    name: instance.name
    state: '"running"'
    health: '(counts.pending ?? 0) > 20 ? "attention" : "ok"'   # ok, attention, down, unknown, maintenance
    attrs: { version: status.version }                          # null results are left out
    caps: [media.requests]
    links: [{ title: Seerr, url: config.url }]
metrics:
  - { resource: app, key: requests.pending, value: counts.pending, unit: count, min: 0, max: 100, band: [0, 5] }
repairs:
  - { when: 'status.version startsWith "0."', code: unsupported_version, message: '"too old"' }
```

Targets and actions, fan-out resources and every key's limits are in [`internal/README.md`](../internal/README.md#integrations), "Tier 1 specs", with the whole annotated sample.

### Backup targets

A backup tool (Backrest, Kopia, Duplicati, borg-ui, Zerobyte and the like) can put what it backs up on Cha Dash's coverage map. Declare `backups.targets` in `meta.capabilities`, and publish one resource per backup plan or repository, usually with `each`, with `caps: [backups.targets]` and kind `backup_target`. Cha Dash reads these attributes:

| Attribute | What | Example |
|---|---|---|
| `backup.covers` | What the target backs up: a list, each `{host, path}` or `{resource}`, or a string meaning a host. At most 32 | `[{host: "10.20.30.40", path: "/mnt/user/appdata"}]` |
| `backup.last_ok` | When its newest good run ended, RFC 3339 | `2026-10-31T03:12:00Z` |
| `backup.last_at`, `backup.last_status` | When its newest run ended, and `ok` or `failed` | `failed` |
| `backup.last_error` | Why that run failed, one line | `repository is locked` |
| `backup.schedule` | How often it runs: `hourly`, `daily`, `weekly`, `monthly`, a Proxmox calendar event (`mon..fri 02:00`) or cron's five fields | `0 3 * * *` |

How `backup.covers` joins: `{resource}` names a resource by its ID. `host` is an address or a name; a name counts only when exactly one host or guest has it. Without a path, or with `/`, the host or guest itself is covered. A path on an Unraid host covers its shares (`/mnt/user`, or `/mnt/user/<share>`) and its flash drive (`/boot`), and on any host the containers whose every mount is under it. The objective follows `backup.schedule` (26 hours for daily, 8 days for weekly) unless someone sets one in Settings → Backups. A target that reports a failed run with an error says so on the things it covers.

```yaml
resources:
  - id: plan
    each: plans.list
    key: item.id
    kind: backup_target
    name: item.id
    caps: [backups.targets]
    attrs:
      backup.covers: 'map(item.paths, {host: config.host, path: #})'
      backup.last_ok: item.lastSuccess
      backup.schedule: item.schedule
```

### Rules the engine enforces

- Requests go only under the instance's URL: an absolute path, no `..`, no other host. Responses must be JSON and at most 1 MiB.
- Secrets go only into the headers, queries and request bodies the spec names, never into paths, and never into errors or logs.
- Expressions are [expr-lang](https://expr-lang.org), compiled at load: a typo fails `spec lint`, not a sync. They see the config (secrets left out), the instance and what the requests extracted, nothing else.
- At most 16 actions, each with its tier (T0 to T4). The hub's roles, step-ups and approvals apply to them as to any action.
- A 401 or 403 is a Repair on the secret; a 404 or 410 is "the API changed", unless one of the spec's repairs explains it.

## Share it

The community repository is `ban-red/cha-dash-specs`: one folder per spec with its YAML, recorded fixtures (required), a README and a recipe for a least-privilege credential. CI runs `spec lint` and `spec test`, and a maintainer reviews every pull request. Cha Dash doesn't fetch from it yet: copy a spec into Settings.
