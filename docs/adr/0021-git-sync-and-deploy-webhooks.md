# ADR-0021 · Stacks follow git through the deploy pipeline; webhooks only poke

**Status:** accepted · **Date:** 2026-10-22 · **Roadmap:** [decision 21](../plan/04-roadmap.md#decisions) · **Plan:** [09 · v0.3 → Git sync and deploy webhooks](../plan/09-v0.3.md#git-sync-and-deploy-webhooks)

## Context

People keep compose files in their own git (Forgejo, Gitea, GitHub) and want a push to redeploy. Cha Dash already versions each node's stacks root with go-git and deploys through a planned, health-gated pipeline that rolls back on failure ([ADR-0020](0020-node-deploys-with-compose-sdk-and-go-git.md)). Two things make this a decision: how a remote's history meets the local repo's, and how a signed request from outside may cause a deploy without weakening "no unauthenticated listeners" or the approval rules for T2 actions.

## Decision

**A synced stack treats the remote as an input, never as history to merge.** The node fetches the configured branch into a cache outside the stacks root, reads one file at the tip, and the hub deploys that content through the existing pipeline. The local repo stays the record of what ran; each deploy commit names its source in a `Chadash-Source: <url>@<sha>` trailer. A failed health gate rolls back as today and marks that SHA rejected, so sync waits for a newer commit instead of retrying it.

- **The node fetches; the hub never links git.** New verbs `stack.remote.check` and `stack.fetch` sit behind a new `stacks.sync` grant (default on: it reads a remote, and deploying still needs `stacks.deploy`), with an optional `stacks.remotes` host allowlist. Sync only pulls: pushing the local history to a mirror is on the backlog, and would get its own grant, off by default.
- **The remote's credential is a vault secret**, sent per call and kept in memory, like stack secrets. URLs with credentials in them are refused. SSH host keys are pinned when an admin confirms the first check.
- **A sync acts as the admin who set it up**, with a new actor kind `sync`, never above that user's current role. If the user drops below admin or is disabled, the sync pauses and the Inbox says so.
- **Two modes per stack.** *Deploy on push* runs without approval; turning it on needs a fresh step-up and is audited. *Draft and ask* (the default) saves the new content as the stack's draft and submits it to the existing approvals flow. A sync always asks when the new content references a stack secret outside its allowlist, and Cha Dash's own stack can't follow git at all.
- **A webhook is a poke, not a payload.** `POST /api/hooks/{id}` takes an opaque hook ID that belongs to a remote and branch, so one hook triggers every stack that follows them. It checks the one header its sender uses, then schedules a fetch. The headers are an HMAC-SHA256 signature (GitHub's `X-Hub-Signature-256`, Gitea's and Forgejo's signature headers, or Cha Dash's own timestamped `X-Chadash-Signature` for CI), or GitLab's `X-Gitlab-Token`, compared in constant time. Nothing in the body is trusted except an optional ref filter. A forged or replayed poke can at most cause a fetch of what is already there, which is also why GitLab's unsigned token is enough.
- **Polling is the baseline** (every 5 minutes by default, checking the tip before fetching). Webhooks make it faster, never different.

## Consequences

- Web edits to a synced stack are blocked: the editor is read-only and says where to edit. A rollback from the web pauses the sync ("held at <commit>") until someone resumes it.
- The authz table gets a new access kind, `Signature`, so a signed route is declared as such rather than as `Public`. The hardening rule becomes "no listeners or routes without authentication": hook routes answer only to a valid signature, and only once a hook exists.
- Public forges need the hub reachable for webhooks. The docs recommend polling, or exposing only `/api/hooks/` through the reverse proxy.
- Only `compose.yaml` syncs in v0.3: no `.env`, no files the stack mounts (the same gap ADR-0020 notes for local stacks).
- GitLab's token proves less than a signature, and a reverse proxy that logs request headers would leak it. The webhook docs say so.
- New tables for syncs and hooks, a `source` on drafts, and audit records for every sync change, every hook rotation and every fetch that finds a new commit.

## Alternatives considered

- **Merge or reset the local repo to the remote.** Fails on any local commit (rollbacks, adopts), and a reset would erase the record of what was tried.
- **Make the remote the stacks root itself.** One remote per node, every stack forced into it, and history rewrites on the remote would rewrite what ran.
- **Deploy what the webhook body says.** Puts trust in the payload; a stolen hook secret would become a remote-code path instead of a fetch.
- **Webhook deploys as API tokens.** Tokens always wait for approval at T2, which defeats deploy on push, and a token in a CI secret is a broader credential than a per-hook HMAC key.
- **Have the hub fetch with go-git.** Breaks the rule that only the node links git and compose, and puts outbound fetches on the hub.
