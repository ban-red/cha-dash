# Live verify: the integrations against a real homelab

The integrations are contract-tested against recorded and synthetic fixtures. This check runs them once against the real thing, the way the hub would, and prints what came back. It's for the maintainer to run by hand. It never runs in CI or in a normal `go test`: it's behind the `live` build tag **and** needs `CHADASH_LIVE=1`.

```bash
CHADASH_LIVE=1 go test -tags live -run TestLive -v -count=1 -timeout 20m ./spikes/live-verify/
```

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
4. Prints a **sanitized** summary per instance and writes the full results to `~/.local/share/chadash-spikes/live-verify/<time>/` (folder 0700, files 0600).

## Read-only
It makes only the requests the integrations make: GETs, plus Unraid's GraphQL *queries*. It never sends `disks` to Unraid, so drives stay spun down. Each integration's README lists its requests. Nothing is written to any source, and nothing is stored in the repo.

## What it prints
- version, test latency;
- grants (permission paths cut after their first segment), warning codes;
- per sync, the time and any failure's code and class;
- resource counts by kind and health, relation counts by type;
- each metric key with how many values are known or unknown, and their range;
- incidents by class, scenario and severity.

No hostnames, IPs, names or secret values appear in the summary. Error and warning **messages** can name hosts, so they go only to the raw files.

## What to expect on the dogfood lab
- **Unraid** fails its syncs with `unsupported_version (needs repair)` until the API is 4.37.4 or later. That's the version gate working.
- **Frigate** on the internal port raises its anonymous-admin hint.
- **Docker on jupiter** raises the broad-proxy hint.
- **Proxmox** with PVEAuditor tokens lists `updates_hidden` and `backups_from_tasks` as test warnings: things a read-only token can never see.

## Summarizing a saved run again
The raw results keep everything the summary reads, so a saved run can be summarized again without touching the lab:

```bash
go test -tags live -run TestFrom -v -count=1 ./spikes/live-verify/ -args -from ~/.local/share/chadash-spikes/live-verify/<time>
```

`TestSummaries` runs the summary code against a local check, offline, to catch a summary that would print "none" for data that's there (`go test -tags live -run TestSummaries ./spikes/live-verify/`).

## Options
| Flag (after `-args`) | Default | |
|---|---|---|
| `-only proxmox/pluto,traefik` | all | run some instances only |
| `-tls verify\|tofu\|insecure` | `tofu` | TLS mode for `https://` sources; the lab uses self-signed certificates |
| `-subscribe 20s` | `20s` | how long to hold push streams; `0` skips them |
| `-gap 5s` | `5s` | pause between the two syncs |
| `-env path` | `../../.env.local` | the env file |
| `-raw dir` | `~/.local/share/chadash-spikes/live-verify/<time>` | where the full results go |
| `-from dir` | | `TestFrom` only: the saved run to summarize |

For example: `CHADASH_LIVE=1 go test -tags live -run TestLive -v -count=1 ./spikes/live-verify/ -args -only traefik -subscribe 0`.
