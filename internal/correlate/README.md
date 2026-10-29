# Correlator (`internal/correlate`)

One thing in the home is often reported by several sources: Sonarr's container by Docker and by Unraid, a guest by two Proxmox instances on one cluster, a backend by Traefik and by Docker's published ports. The correlator runs in the hub over the live Home Graph and says which reports are the same thing, and how things from different sources relate. It never deletes or rewrites what a source published.

## The design: a primary per identity, `same_as` to it

Every resource the sources publish stays in the graph, with its own `source`, attrs and health. When several resources are one thing (an **identity**), the correlator picks one of them as the **primary**, and publishes `<member> same_as <primary>` for each other member. Nothing else is added: no copied resources, no merged values.

That gives readers three cheap questions:

| Question | Answer |
|---|---|
| One of each thing | the resources with no outgoing `same_as` (`Distinct`) |
| What is this resource? | its `same_as` target, or itself (`Canonical`) |
| What does every source say about it? | the primary plus whatever is `same_as` it (`Members`), each with its own `source`: that's the provenance |

A widget that lists containers shows the primaries and, for each, merges what its members say (the worst health, every source's name). The web app does that in `web/src/model/graph.ts` (`aliases`, `members`, `byKind`, `engines`); hub code uses `Distinct`, `Canonical` and `Members`.

**Why not a canonical resource with merged fields?** It would copy every member's state into a second resource, so every change of a member would be two resource diffs on the wire, and merged fields need per-field precedence rules that differ by reader. With `same_as` the wire carries one small relation per merge, and readers decide what to merge.

**The primary** is chosen by source: Docker for a container (Unraid folds `created` into `exited`), the built-in checks for a check, then Proxmox and Unraid, then anything; ties go to the smaller ID. It's stable while the members are. Every relation the correlator publishes points at primaries, so a route goes to Docker's container, never to Unraid's view of it.

All the correlator's relations carry `source: "correlator"`. It ignores them as input, so its output never feeds back. It only adds relation kinds no integration publishes (below), and never takes over or removes a relation a source published.

## What it publishes

| Relation | From → to | Rules |
|---|---|---|
| `same_as` | member → primary | container ID (a 12-character ID matches a full one by prefix); VMID on the same node (nodes merged first, so two standalone hosts with a VMID 100 stay apart); disk serial; `chadash.id` label on two resources of one kind; host address (same platform); engines listing the same containers; app instances of one type at one URL; Plex's machine identifier; checks of one target |
| `runs_on` | engine → host or guest | the host whose integration reports the engine's containers by ID (Unraid); the host or guest at the engine's address; the one host or guest with the engine's host name and the same cores and memory (an engine reached through the node has no address) |
| `runs_on` | app → container | a container labelled `chadash.id=<app id or instance id>`; a container whose `homepage.href` or `homepage.widget.url` is the app's URL; a published port (or bridge address) the app's URL reaches, when the URL has no path; the container behind the route serving the app's URL, unless that route's backend has a path (another proxy) |
| `runs_on` | app or router → host or guest | the app's URL host (no path), or the router's `ip`, is the host's or guest's address, and no container matched |
| `routes_to` | route → container | the route's `upstream_addr` is a port a container publishes on its engine's addresses (its own, or its host's), or a container's bridge address and port; or the container's `homepage.href` is the route's URL |
| `routes_to` | route → app | the app's URL falls under the route's open URL (the most specific route); the route's backend URL is the app's URL; or the route goes to a container where exactly one app runs |
| `monitors` | check → app, route, container | the check's target is the app's URL, falls under the route's URL, or is a port the container publishes |
| `stores_on` | PVE storage → PBS datastore | the storage's `pbs.server` is a host of the PBS instance's URL and `pbs.datastore` is the datastore's `store` |
| `depends_on` | container → container | the Compose label `com.docker.compose.depends_on`, within one project on one engine |
| `depends_on` | engine → Unraid array | an Unraid-managed engine on an Unraid host: Unraid runs Docker only while the array is started |

**Addresses.** A container answers at `host:port` for each published port, where `host` is a binding's own IP, or else each of the engine's addresses: its endpoint's IP or DNS name, plus the address of the host or guest the engine runs on. That's why placing the engine comes first: on the dogfood home the engine is reached through `tcp://dockerproxy:2375`, and Unraid's container IDs place it on jupiter, whose address then makes Traefik's `10.0.11.120:6767` the bazarr container. Loopback, unspecified and link-local addresses never match anything: `127.0.0.1` is every machine.

**`backed_up_by`** isn't published across sources: no v0.1 integration reports which PBS snapshots belong to which guest. Proxmox's own `guest backed_up_by job` and `job stores_on storage`, with the correlator's `storage stores_on datastore`, already lead from a guest to its datastore.

## Confidence

Every link carries its `rule`, a `confidence` and a sentence of `evidence` (`GET /api/correlate/links`).

| Confidence | What | Published? |
|---|---|---|
| `exact` | the same identifier seen twice: container ID, VMID on one node, serial, `chadash.id`, a Compose dependency | yes |
| `strong` | one network identity or an explicit label: an address and port, a route's backend, `homepage.href` | yes |
| `confirmed` | the owner said so | yes |
| `weak` | names alike: an app and a container (or its Compose service, or its image) with the app's name; the only two apps of a type on the same version; an engine reporting a host's name without the same cores and memory, or one several hosts or guests share; a PBS datastore name only one PBS has | no: an Inbox suggestion |

Relations that can only have one answer (what an app runs on, which container a route goes to, which datastore a storage writes to) are **slots**: the strongest candidate wins, and a tie between different targets is ambiguous, so up to three of them become suggestions instead of a link (Docker's default bridge gives every engine a `172.17.0.2`).

## Suggestions and decisions

A weak or ambiguous match becomes an inbox item on the `inbox` topic:

- `id` `correlate.<16 hex>`, stable for the pair;
- `class: "suggestion"`, `severity: "info"`: never a problem, never in Needs attention;
- `scenario` `merge-suggestion` ("These look like the same thing. Merge?", for `same_as` and for an app and its container) or `link-suggestion` ("Does … run on …?");
- `target` the first resource; `facts` `from`, `to`, `relation`, `rule`, `confidence`; `summary` the evidence;
- `suggested` `correlate.confirm` ("Merge" or "Link them"), `alternatives` `correlate.reject` ("Keep separate").

Answered, it resolves (`resolved_by: "action"`, with a resolution sentence) and stays a day, like a resolved incident. A suggestion whose evidence disappears leaves the inbox.

**Decisions persist** in `correlation_decisions` (migration `00005`): one row per pair and relation type, `same_as` pairs stored with the smaller ID first. A decision outranks every rule: `confirm` publishes the link (confidence `confirmed`) and wins its slot; `reject` keeps the pair apart and the suggestion away. A rejected `same_as` also stops the two joining through a third member. The rows reference the instances that publish the two resources, so a decision goes when either instance is deleted.

### API (owner only)

| Route | |
|---|---|
| `GET /api/correlate/links` | `[{"from","type","to","rule","confidence","evidence"}]`: everything published now |
| `GET /api/correlate/suggestions` | `[{"id","from","type","to","rule","confidence","evidence","title","summary","opened_at"}]`, oldest first |
| `POST /api/correlate/suggestions/{id}/confirm` | → `200` the decision. `404` when the suggestion isn't open |
| `POST /api/correlate/suggestions/{id}/reject` | → `200` the decision |
| `GET /api/correlate/decisions` | `[{"id","from","type","to","verdict","rule","created_at"}]`, oldest first |
| `POST /api/correlate/decisions` | `{"from","type","to","verdict"}` → `201` the decision: merge or split any pair, such as a wrong automatic merge. `422 invalid`; `404` when a resource isn't in the graph. Deciding a pair again replaces the decision |
| `DELETE /api/correlate/decisions/{id}` | → `204`; the rules judge the pair again. `404` |

## Incidents across instances

One instance can't see another's incidents. The correlator links them without making new ones: it adds causes to the incidents the instances raised.

An open problem's **roots** are the PBS datastores (or PBS) its target and causes lead to: a backup job's storage, a guest's jobs' storages, the storage itself, then `stores_on` the datastore. Without a joined datastore, the incident's `facts.endpoint` finds the PBS answering there; without any PBS, the endpoint itself is the root. A PBS incident is about its root (its target), and a filling incident's `facts.stores` names every datastore on the shared filesystem.

- **A PVE backup failure** gains, in front of its own causes, one cause pointing at the datastore when PBS has a problem of its own there: its open incident ("It writes to PBS datastore mars on PBS, which has a problem of its own: PBS datastores are filling up"), else the datastore's health ("… which is 92% used · full in about 3 weeks"), else Cha Dash failing to read that PBS too. Then one cause per other instance whose backups to the same PBS failed (up to three), pointing at that instance's job. With only an endpoint: "Backups on pluto can't reach 10.0.11.120:8007 either".
- **The PBS incident** gains the backup jobs that write to its datastores in its blast radius (`via: "stores_on"`).

The correlator remembers what it added. Each pass reads every incident under the inbox's lock (`livedoc.Tx.Item`), takes off what it added before if it's still there, and adds what holds now, so an instance rewriting its incident never loses a write, and the links come back after it. Resolved incidents are left as their instance wrote them.

## Running

`cmd/chadash` starts one correlator over the topics the integration instances write to: the served topics normally, the private set in demo mode (the Demo Home is one source; there is nothing to merge). It correlates 500 ms after the graph or the inbox changes, and again once after its own changes (which find nothing new). Snapshots are taken fresh each pass. A pass over the Docker, Unraid and Traefik captures together (316 resources, 483 relations) takes about 0.9 ms on an M-series laptop. An inbox change alone (the clock ticks every 30 s) skips the matching and only refreshes the inbox.

## Not yet

- **Per-tile identity.** Widgets bound to an alias ID aren't redirected to the primary; the web resolves by primary when it lists.
- **DNS.** Names are compared as written; nothing resolves `jupiter.lab.example` to `10.0.11.120`. A route whose backend names a host while the engine is known by address doesn't match (the import record's hrefs and `homepage.href` labels often cover it).
- **The nginx-proxy hop.** A route through another proxy reaches the proxy's container; the app behind it is only suggested by name. Following it needs `VIRTUAL_HOST`/`VIRTUAL_PATH`, which the Docker integration drops.
- **More keys.** MAC addresses (OPNsense leases ↔ guests), the Proxmox cluster CA as an identity, OPNsense `provides_network_for`, Home Assistant ↔ Frigate `depends_on`, PBS snapshot groups for `backed_up_by`.
- **The import record's `runs_on` links and discovery entries** still live in the record; they could become confirmed decisions.
