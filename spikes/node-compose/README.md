# Spike 4.6 · node prototype: owning compose stacks

Throwaway code that answers one question: **can `chadash-node` own compose stacks with the Compose SDK and go-git**, safely enough to build the v0.1 node on? It runs on the [sandbox](../node-sandbox/README.md) as root and walks the sample stack `hello` through adopt → diff → deploy → health gate → rollback.

Not product code. The findings go into the notes below and into ADRs; the real node is written fresh.

## How it works

| Step | What happens | Built on |
|---|---|---|
| **Adopt** | Commits the stack's compose file to a git repo at the stacks root (`/opt/stacks`), **in place**. Nothing restarts, and the project name and volumes stay. `.env` files are git-ignored, so secrets never enter history | go-git |
| **Diff** | Three views of a proposal: <ul><li>a line diff of the text</li><li>a static plan comparing each service's config hash with the label Compose stamped on the running container</li><li>a **dry run** through Compose's own reconciler</li></ul> | `compose.ServiceHash`, `compose.WithDryRun` |
| **Preflight** | The dry run also resolves every image against its registry. If it fails, the deploy is **blocked** and nothing is touched | dry-run client |
| **Deploy** | Writes the file, commits `Deploy <stack>`, runs `up` with orphan removal | `api.Compose.Up` |
| **Health gate** | `up --wait`: every container running, and healthy where a healthcheck exists. Then a **soak**: every container must *stay* running and not unhealthy, which catches crash loops `--wait` can miss | `StartOptions.Wait`, `Ps` |
| **Rollback** | Restores the previous file as a **new commit** (`Roll back <stack>: <reason>`) and runs it through the same gate. Failed attempts stay visible in the history | go-git |

Exit codes: `0` deployed · `2` rolled back · `3` stuck (the rollback failed too) · `4` blocked by the dry run.

## Run it

1. Your SSH key must be on the sandbox. If `ssh root@<sandbox>` asks for a password, see [SSH in the sandbox README](../node-sandbox/README.md#ssh-permission-denied-publickey).
2. **▶ You run** (or tell me to):

```bash
spikes/node-compose/run.sh
```

It cross-compiles for the sandbox's architecture, copies the binary and scenarios to `/root/node-spike`, runs [`run-scenarios.sh`](run-scenarios.sh) there, and saves the log to `bin/` (git-ignored). The runner resets `hello` to its baseline first, so it can be re-run. It refuses to run on any host other than `chadash-sandbox`.

## Scenarios

| # | Change | Expect |
|---|---|---|
| — | Adopt `hello` | The same container keeps running |
| — | Diff the running file against itself | Identical, `web` unchanged. Proves our hash matches the one the CLI stamped |
| 01 | nginx 1.27 → 1.28 | Deployed (exit 0) |
| 02 | Healthcheck probes a port nothing listens on | Gate fails → rolled back to 01 (exit 2) |
| 03 | Process exits after 3 s, restart policy loops it | `--wait` passes; the soak sees the restart → rolled back (exit 2) |
| 04 | Image tag doesn't exist | Blocked by the dry run, nothing touched (exit 4) |
| 05 | Add a `whoami` service | Deployed; `web` left alone (exit 0) |
| 06 | Back to the baseline | Deployed; `whoami` removed as an orphan (exit 0) |

The summary also shows, per deploy, how long `:8080` was unreachable (a probe every ~250 ms) and the node's peak memory.

## What we're measuring

1. **Hash parity:** can the node predict recreations statically, without asking Docker?
2. **Dry run as plan and preflight:** is it accurate, and does it catch a missing image before anything stops?
3. **Gate quality:** is `--wait` plus a soak enough to catch unhealthy services and crash loops?
4. **History:** is "rollback as a new commit" readable as an audit trail?
5. **Downtime:** Compose recreates by stopping and then starting. How long is the gap, and does a failed deploy cost more?
6. **Cost:** binary size, peak memory, dependency count, and the Go floor. Compose v5.6 needs Go ≥ 1.26.8, and pulls in docker/cli and moby.

## Findings

Run on 2026-10-03 against the sandbox (Debian 13 LXC on mars-2, Docker 29.8.2, Compose v5.6.0). Run 2 below. Run 1 differed only where noted.

**Verdict: build the v0.1 node on the Compose SDK and go-git.** All eight checks passed on both runs, with no shelling out to the docker CLI.

| | Result |
|---|---|
| Adopt in place | ✅ Same container ID before and after; nothing restarted |
| Hash parity | ✅ `compose.ServiceHash` reproduced the CLI's label exactly. Recreations can be predicted from labels alone |
| 01 upgrade | ✅ Deployed in 27.8 s, including the 20 s soak (36.7 s in run 1, which also pulled the image) · down **1.5 s** |
| 02 failing healthcheck | ✅ Gate failed in **8.8 s** ("unhealthy"); rolled back and healthy 36.6 s after start · down 3.1 s |
| 03 crash loop | ✅ `--wait` reported it **"Healthy"**; the soak caught "restarted 1×" in **6.8 s** and rolled back · down **10.0 s** |
| 04 missing image | ✅ **Blocked** by the dry run (`manifest unknown`); nothing touched · down **0 s** |
| 05 add a service | ✅ `whoami` created; `web` left running · down **0 s** |
| 06 back to baseline | ✅ `whoami` removed as an orphan, `web` recreated · down 1.5 s |
| CLI coexistence | ✅ `docker compose ls` still shows the stack at its real path after node deploys |
| Cost | **31 MB** stripped static binary · 702 packages, 386 modules · peak RSS **28–36 MB** per command · Go ≥ 1.26.8 |

**What we learned**

1. **Static plans work.** Because the hashes match, the hub can show "web will be recreated" as you edit, without the node touching Docker. Parity holds per Compose version, though. Pin the SDK, and if a host's CLI-created containers don't match on adopt, say "the next deploy recreates everything once" up front.
2. **The dry run is a real preflight.** Its plan matched what each deploy actually did (recreate, create, remove, untouched), and it caught the bad tag at zero cost. Two quirks the UI must hide:
   - it says `Pulled` for images it only resolved;
   - it names recreated containers `<id>_name`.

   It needs the registry to be reachable, so "couldn't check" must look different from "would fail".
3. **`--wait` isn't a gate on its own.** For a service without a healthcheck, `--wait` means "running", and Compose still prints "Healthy". Run 1 polled state during the soak and caught the crash loop only at 18.6 s (down 22.7 s). Run 2 watches the **restart count** and caught it at 6.8 s (down 10.0 s). The UI should say "running · no healthcheck", never "healthy", for those services.
4. **Gate latency is the service's own healthcheck.** 02 failed in 8.8 s because its check runs every 3 s and allows 2 retries. A service checked every 30 s takes minutes to fail. Suggest a tighter `start_interval` when the node sees long intervals.
5. **Rollback as a new commit reads like an audit log.** Run 2's subjects carry the plan and the reason:
   ```
   bdf214d  Roll back hello: hello-web-1 restarted 1× during the soak
   c7be50a  Deploy hello: recreate web
   037c5d1  Roll back hello: container hello-web-1 is unhealthy
   43726da  Deploy hello: recreate web
   ```
6. **Recreate means stop, then start.** That's about 1.5 s of downtime per recreated service. A failed deploy costs two recreates if the broken version still serves, and the whole detect-and-rollback window if it doesn't. Untouched services and blocked deploys cost nothing. For v0.1, show the estimate on the confirm sheet ("web restarts, ~2 s"). Start-first updates for stacks behind Traefik are a later feature.
7. **"Thin" is relative.** 31 MB and about 35 MB of memory comes from linking docker/cli and moby. That's comfortable on every host in the lab and small next to a 1 GB board's memory, and it's a data point for the hardware floor (ADR-0016). The hub never links the SDK. A long-running node's resident memory still needs measuring.
8. **Rollback costs a full soak.** Of the ~36 s from a failed deploy's start to healthy again, 20 s is the rollback's soak. The previous version has already proven itself, so a shorter rollback soak (5 s) is reasonable.

**Carry into the real node:** record image digests at deploy and pin them on rollback; a per-stack lock; soak on restart count (or Docker `die` events) with a per-stack soak length in `x-chadash`; refuse to deploy over drift and offer "accept edits"; commit subjects built from the plan; "stuck" becomes an Inbox item.

## Known gaps (deliberately out of scope)

- One compose file per stack; no `include`, overrides or profiles.
- Files the stack mounts (configs next to `compose.yaml`) aren't versioned yet; only the compose file is.
- Rollback restores the *file*, so a moved tag (`nginx:1.28-alpine` updated upstream) can roll back to a different image. The real node should record the digests it ran (Compose's `com.docker.compose.image` label) and pin them on rollback.
- No per-stack lock, no policy file, no transport: the hub isn't involved yet.
