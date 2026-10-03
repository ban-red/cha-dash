# ADR-0020 · The node deploys with the Compose SDK and go-git

**Status:** accepted · **Date:** 2026-10-03 · **Evidence:** [spike 4.6 findings](../../spikes/node-compose/README.md#findings)

## Context

Owning stacks ([ADR-0006](0006-owned-stacks-and-thin-node.md)) means the node has to deploy compose files safely: predict what will change, block bad deploys before anything stops, notice an unhealthy result, and roll back with a readable history. Spike 4.6 walked a sample stack through adopt → diff → deploy → health gate → rollback on a sandbox LXC (Docker 29.8, Compose v5.6). All eight checks passed, without the docker CLI or a git binary.

## Decision

**The node runs compose in-process through the Compose SDK (`docker/compose/v5`) and versions each stacks root with go-git.** The [deploy pipeline](../plan/03-architecture.md#stacks-cha-dash-owns-them-):
1. **Plan statically.** Compare each service's config hash (`compose.ServiceHash`) with the label on its running container. It matched the CLI exactly, so the hub can say "web will be recreated" while you edit.
2. **Preflight with a dry run** through Compose's own reconciler. It resolves every image against its registry, so **a bad tag blocks the deploy before anything stops**. "Couldn't check" (registry unreachable) looks different from "would fail".
3. **Commit the attempt, then `up --wait`.**
4. **Soak, restart-aware.** Every container must stay running and not unhealthy, and any restart fails the gate. `--wait` alone passed a crash loop as "Healthy".
5. **Roll back as a new commit**, pinned to the image digests the previous version ran, through the same gate with a shorter soak. Failed attempts stay in the history, with the plan and the reason in the commit subject.
6. **Stuck** if the rollback fails too: a high-severity Inbox item.

The real node also gets a per-stack lock, a per-stack soak length in `x-chadash`, a refusal to deploy over drift (with "accept edits"), and a pinned SDK version.

## Consequences

- **Cost:** a **31 MB** static binary and about **35 MB** peak RSS per command (28–36 MB measured), from linking docker/cli and moby. That misses the node budget in [03 · Architecture](../plan/03-architecture.md#performance-budgets-enforced-in-ci-where-possible) (≤ 15 MB binary, ≤ 20 MB idle RSS), so the budget needs revising. It's also a data point for the hardware floor (ADR-0016). A long-running node's idle memory still needs measuring.
- **Go ≥ 1.26.8** for the SDK. The repo's Go 1.27.1 toolchain covers it.
- Only the node links the SDK. The hub never does.
- **Downtime:** a recreate stops, then starts, about 1.5 s per recreated service. A failed deploy costs more (10 s for the crash loop). Untouched services and blocked deploys cost nothing. The confirm sheet shows the estimate.
- Services without a healthcheck show as "running · no healthcheck", never "healthy". Gate speed depends on each service's own healthcheck interval.
- Hash parity holds per Compose version. If adopted containers don't match, the UI says up front that the next deploy recreates everything once.
- Not covered yet: `include`, overrides and profiles, and versioning files a stack mounts from next to its `compose.yaml`.

## Alternatives considered

- **Shell out to the docker CLI and git.** Familiar, but bigger images, parsing text output, and version skew between hosts.
- **Reimplement compose on the raw Docker API.** A smaller binary, but Compose's reconciler is a project of its own, and hash parity with the CLI would drift.
- **Use `up --wait` as the whole health gate.** Measured in the spike: it passes crash loops.
- **Roll back by resetting git history.** Simpler, but it erases the record of what was tried.
