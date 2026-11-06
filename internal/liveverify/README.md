# Live verify: the integrations against the real lab

Layer 5 of [17 · Test automation](../../docs/plan/17-test-automation.md). The integrations' contract tests replay recorded fixtures. This runs them once against the maintainer's lab, read-only, the way the hub would. It compares what the lab answered with those recordings and prints a sanitized summary. It isn't a test of Cha Dash: it finds out when an upstream API (or the lab) has moved on, so the fixtures no longer match.

```bash
bun run live:verify                          # every instance in .env.local
bun run live:verify proxmox/pluto traefik    # some
bun run live:verify docker/jupiter -subscribe 0
```

It's behind the `live` build tag **and** `CHADASH_LIVE=1` (the script sets it), so it never runs in CI or in a normal `go test`. The agent may run it without asking ([T2](../../docs/plan/17-test-automation.md#decisions)).

## What it does
1. Reads the credentials from `.env.local` at the repo root (`-env` to point elsewhere).
2. Builds one instance per source it finds there:
   - `PVE_<NAME>_URL` / `_TOKEN_ID` / `_TOKEN_SECRET` → `proxmox`
   - `DOCKER_<NAME>_URL` → `docker` (`http://host:port` becomes `tcp://host:port`)
   - `TRAEFIK_URL` (with `TRAEFIK_USERNAME` and `TRAEFIK_PASSWORD`) → `traefik`
   - `OPNSENSE_URL` / `_KEY` / `_SECRET` → `opnsense`
   - `FRIGATE_URL` → `frigate`
   - `UNRAID_<NAME>_URL` / `_API_KEY` → `unraid`
3. For each instance, runs it on the hub's real runtime (HTTP clients with the TLS policy, emitter, scrubbing) through `internal/integration/integrationtest`:
   - `Test`;
   - two `Sync`s, 5 s apart, as if a board were open;
   - for integrations that push, the stream for 20 s (OPNsense's SSE traffic, Frigate's `/ws`, Docker's events).
4. **Compares** every JSON answer to a GET with the recording of the same route (`shape.go`, `recorded.go`). The comparison is by shape: which fields there are and what type each holds, never values. Array elements merge, and objects keyed by data (paths, labels, token names) merge into `.*`.
5. Prints a **sanitized** summary per instance and writes the full results to `~/.local/share/chadash/live-verify/<time>/` (folder 0700, files 0600).

**It fails** when a recorded field is gone, or its type changed, on any route. The fix is new fixtures (sanitized) and a layer 1–2 test that fails on the old ones. Fields only the lab has are counted, not failed: an API that grew isn't broken.

## Recordings
| Integration | Recording | Routes |
|---|---|---|
| `proxmox` | `testdata/integrations/proxmox/<name>/_index.json` | each request's path, names and IDs as `{}` |
| `docker` | `testdata/integrations/docker/<name>/`: `version.json`, `containers.json`, `inspect/*.json` | `/version`, `/containers/json`, `/containers/{}/json` |

Others print "no recordings to compare yet". The instance's name matches the folder without dashes (`PVE_JUPITERNAS_URL` is `jupiter-nas`). When the recording names its version and the instance now runs another, the summary says so.

## Read-only
It makes only the requests the integrations make: GETs, plus Unraid's GraphQL *queries*. It never sends `disks` to Unraid, so drives stay spun down. Each integration's README lists its requests. Nothing is written to any source, and nothing is stored in the repo.

## What it prints
- version, test latency;
- grants (permission paths cut after their first segment), warning codes;
- per sync, the time and any failure's code and class;
- resource counts by kind and health, relation counts by type;
- each metric key with how many values are known or unknown, and their range;
- incidents by class, scenario and severity;
- drift: routes compared, and each recorded field that is gone or changed type, by its recorded name.

No hostnames, IPs, names or secret values appear in the summary. Error and warning **messages** can name hosts, and new fields can name things in the lab, so they go only to the raw files.

## What to expect on the dogfood lab
- **Unraid** fails its syncs with `unsupported_version (needs repair)` until the API is 4.37.4 or later. That's the version gate working.
- **Frigate** on the internal port raises its anonymous-admin hint.
- **Docker on jupiter** raises the broad-proxy hint.
- **Proxmox** with PVEAuditor tokens lists `updates_hidden` and `backups_from_tasks` as test warnings: things a read-only token can never see.

## Summarizing a saved run again
The raw results keep everything the summary reads, so a saved run can be summarized again without touching the lab:

```bash
go test -tags live -run TestFrom -v -count=1 ./internal/liveverify/ -args -from ~/.local/share/chadash/live-verify/<time>
```

`TestSummaries` runs the summary code against a local check, offline, to catch a summary that would print "none" for data that's there (`go test -tags live -run TestSummaries ./internal/liveverify/`). The comparison has its own offline tests, without the tag: each recording matches itself, and a dropped or retyped field is found..

## Options
| Flag (after the instances) | Default | |
|---|---|---|
| `-only proxmox/pluto,traefik` | all | run some instances only |
| `-tls verify\|tofu\|insecure` | `tofu` | TLS mode for `https://` sources; the lab uses self-signed certificates |
| `-subscribe 20s` | `20s` | how long to hold push streams; `0` skips them |
| `-gap 5s` | `5s` | pause between the two syncs |
| `-env path` | `../../.env.local` | the env file |
| `-raw dir` | `~/.local/share/chadash/live-verify/<time>` | where the full results go |
| `-fixtures dir` | `../../testdata/integrations` | the recordings to compare with |
| `-from dir` | | `TestFrom` only: the saved run to summarize |
