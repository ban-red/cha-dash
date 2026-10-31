# internal/stacks

The hub's side of owned stacks:
- the stacks API over the nodes (`nodes.Caller`);
- drafts in the hub's database;
- stack secrets in the vault;
- the stack actions the engine runs.

The node does the work ([`internal/node/stacks`](../node/stacks/README.md)). This package speaks its verbs through [`internal/stackproto`](../stackproto) and never links the Compose SDK or go-git.

## Routes

| Route | Role | Notes |
|---|---|---|
| `GET /api/stacks` | viewer | Per node: `{node, name, online, error, root, stacks, detected}`. A node that's offline or fails says so in `error` and still lists its drafts |
| `GET /api/stacks/{node}/{stack}` | admin | The committed file, `.env` key names, `x-chadash`, services, history (50), head, draft, and referenced secrets with `set`. A stack that's only a draft answers with `exists: false` |
| `PUT /api/stacks/{node}/{stack}` | admin | `{content, revision}` saves a draft. A stale revision gets `409 conflict` with `revision`. A protected stack gets `409 protected` |
| `DELETE /api/stacks/{node}/{stack}/draft` | admin | Discards the draft |
| `POST /api/stacks/{node}/{stack}/validate` | admin | `{content}` → `{ok, messages, secrets, plan, diff}`. The node validates. The hub adds an error for each referenced secret the vault lacks, and plans only when both pass. Saves nothing |
| `GET /api/stacks/secrets` | admin | Stack secret names. Values never leave |
| `PUT`/`DELETE /api/stacks/secrets/{name}` | admin | Set or delete one, audited as `settings.stacks.secret.update`/`.delete` with the name only |
| `POST /api/stacks/{node}/{stack}/move/preview` | admin | `{from_path}` or `{content}` → `{ok, rewrites, plan, path, refusal}`. A dry run of `stack.move` on the node: it stages the files, plans, and takes them away again. A refusal from the node (`exists`, `protected`, `invalid`, `unadoptable`, `not_found`) is `200` with `refusal`, so the review can show it |
| `GET /api/nodes/{id}/self-update` | admin | `{node, allowed, available, reason, running, last}`. `allowed`: the node's policy grants `stacks.selfupdate`. `available`: it also runs in the hub's compose project. `last` is the node's last self-update result |

Node errors map to HTTP:
- `not_found` → 404;
- `invalid` → 422;
- `protected`, `drift`, `conflict`, `busy`, `not_managed` → 409;
- an offline node → `503 node_unavailable`;
- anything else → `502 node_error`.

**Why these roles.** The Roles table puts stacks under Settings, admin+. Listing names and status is a read, so viewers get it. A stack's files are admin+ because compose files, adopted ones especially, often hold credentials in plain text. The actions are admin+ too: `authz.Decide` treats every `stack.*` scope as a setting, although an operator may run other T2 actions.

## Actions

| Action | Tier | Target | Undo |
|---|---|---|---|
| `stack.deploy` | T2 | `stack:<node>/<name>` | `stack.rollback` to the commit it replaced |
| `stack.rollback` | T2 | same, `{"commit"}` | `stack.rollback` back to the commit it replaced |
| `stack.adopt` | T1 | same | `stack.unadopt` (none when it accepts edits) |
| `stack.unadopt` | T1 | same | `stack.adopt` |
| `stack.move` | T1 | same, `{"from_path"}` | `stack.unadopt`, which leaves the moved copy, the old file and the containers alone |
| `chadash.update` | T3 | `node:<id>`, `{"image"}` (a tag of `ghcr.io/ban-red/chadash`) | none. It resumes after the hub restarts |

How the actions work:
- **Resolve** reads the stack from its node. For a deploy it also validates and plans the draft, so the plan's steps name the services ("Pull nginx:1.28", "Recreate web …", the gate, "roll back on its own"). The result travels in `Target.Ref`.
- **The target's state** hashes the head commit, managed, drift and the draft's SHA-256. An approval after anyone saves a new draft or deploys is `409 plan_changed`.
- **Blast radius** is the graph's `stack` resources for the project (the Docker integration's `docker:<key>/stack/<name>`) and `GraphImpact` from each.
- **Steps** lead with what the node flags as root-equivalent on its host (`Plan.RootEquivalent`: privileged, host namespaces, devices, `cap_add`, host paths), when there is any, so the person holding the ring sees it.
- **Execute** reads the draft, checks it's the one planned, and reads each referenced secret from the vault for that one call. It records the names as granted to that stack on that node, sends the deploy with `expect_head`, so a deploy in between is refused, and streams the node's progress into the task. A rollback sends only granted names (see Secrets).
- **Outcomes.** `deployed` succeeds and clears the draft, if it's still the same revision. `blocked`, `rolled_back` and `stuck` fail the task with that code and the node's sentence. `stuck` also opens an Inbox item, `stack.<node>.<name>.stuck`, at severity `down`; the next deploy or rollback that passes the gate resolves it.

## Fleet stacks (`fleet.go`, `fleet_http.go`)

**Move.** `stack.move` takes the compose file the project runs from (`from_path`), which the node checks against Docker's labels or its in-place registration. The node copies it and its `.env` to `<root>/<name>/`, makes relative paths absolute, commits it as an adopt, and runs nothing. The web shows the preview first: each rewritten line and what the first deploy will recreate. Content an importer supplies doesn't go through the action: a plan and its audit record keep their params, and a compose file can hold credentials. The importers, when they come, will need their own path to the node. A moved stack's `GET` carries `moved_from` while the old file is still there.

**Cha Dash updating itself.** `chadash.update` targets `node:<id>`, the node beside the hub; its typed confirmation is "Cha Dash". Preconditions ask the node's `stack.selfupdate.result`: a policy without `stacks.selfupdate` or a node outside the hub's compose project is `unavailable`, and an update already running is refused. Execute sends `stack.selfupdate` with the task ID as `ref`. When the node recreates the hub, the hub shuts down mid-call. The engine leaves a task whose Def resumes running (`action.Def.Resumes`), and at the next start `Resume` asks `stack.selfupdate.result` every 3 s. It finishes the task with the node's outcome once the result carries this task's ID and isn't `running`. A result for another task means the node never started this one: `interrupted`, nothing changed. Outcomes map as deploys do: `deployed` and `unchanged` succeed; `blocked`, `rolled_back` and `stuck` fail with that code.

## Drafts live in the hub

Table `stack_drafts` (migration `00009`): one per node and stack, with a revision for optimistic saves.
- They live in the hub, not as an uncommitted file on the node. The stacks root then only changes by commit, so a file on disk that differs from its last commit always means an edit made outside Cha Dash, and drift stays exact.
- Drafts survive the node being offline.
- They're kept when a deploy fails, so the work isn't lost.
- They hold `${secret:…}` references, never values.
- Each says where it came from in `source` (migration `00017`): `web` for the editor, which every save writes, and `git:<sha>` for a git sync. Drafts from before the column read back as `web`. A draft's JSON (`content`, `revision`, `updated_at`, `updated_by`, `source`) carries it.

## Secrets

Stack secrets are vault entries named `stacks/<name>`, a namespace of their own. Whatever a compose file references, it can't reach an integration's credentials (`instance/…`). They're read only for `stack.plan` (validation's plan), deploys and rollbacks, sent to the node with that request, and cleared from memory afterwards. `TestScenarioBadDeployRollsBackOnItsOwn` checks that the value went only with `stack.plan` and `stack.deploy`. It also checks that no response, task or audit event holds it.

**The hub decides which secrets go.** For validation and deploys, the names come from the hub's own reading of the draft, never from the node's reply. A deploy records them in `stack_secret_grants` (migration `00014`) for that node and stack. A rollback restores a version that lives on the node, so the node says which names it needs; the hub sends them only if each was granted to that stack on that node, and otherwise fails with `secret_not_granted`, naming it. A node can't ask for another stack's secrets, or another node's. The vault namespace is unchanged (`stacks/<name>`): grants scope what goes where, so no secret had to move. `TestRollbackSendsOnlyGrantedSecrets` and `TestPlanSendsSecretsFromTheHubsReading` play a node that lies.

## Git sync

A stack can follow a branch and a path on a git remote ([ADR-0021](../../docs/adr/0021-git-sync-and-deploy-webhooks.md)). The node checks and fetches (`stack.remote.check`, `stack.fetch`); the hub decides.

**One sync** (`sync.go`):
1. **Trigger.** The poll timer (every `poll_seconds`, default 5 minutes, at least 1, with up to 10% jitter), Check now, a resume, or a webhook. `Syncer` runs one goroutine with a ticker, and each sync runs single-flight, with at most one more run queued behind it. It starts and stops with the hub's context.
2. **Owner.** A sync acts for the admin who set it up (`owner_id`), as actor kind `sync` with ID `sync:<id>`. If that user is deleted, disabled or no longer an admin, the sync pauses and the Inbox says so. auth's `user.changed` event pauses it at once.
3. **Check.** If the tip is the last SHA seen, it stops. A new tip that's one of the rejected ones is recorded as seen and audited once as `stack.sync.skip` (refused, with why), and the sync stops there too. For SSH the host key the remote presents must be the pinned one.
4. **Fetch** the file at the tip, and audit `stack.sync.fetch` once per new tip.
5. **Gate.** The fetched commit is checked against the rejected ones again (a push can land between check and fetch). A stack with drift waits (and retries on the next poll); a protected stack pauses the sync; content equal to what runs only records the SHA.
6. **Draft and plan.** The content becomes the draft with source `git:<sha>`, by "git sync". Any older approval the sync is waiting on is withdrawn (`Engine.Withdraw`). The sync plans `stack.deploy` with `RequireApproval` in draft-and-ask mode, or when the file references a stack secret outside `secrets_allow`, or when the node's plan lists anything root-equivalent (`Plan.RootEquivalent`: privileged, host namespaces, devices, `cap_add`, host paths; `stacks.deny` may allow them), or when that plan can't be read. Deploy on push then approves and runs it; otherwise the Inbox shows the approval, and for deploy on push the sync's item says why it asked. A refusal that passes (`locked`: something else runs on the stack; `unavailable`: the node is out of reach) counts as a failed poll and leaves the tip unseen, so the next poll tries it again.
7. **Result** (`executeDeploy`, for any deploy of a git draft): deployed records `last_deployed_sha`; rolled back adds the commit to `rejected_sha` and opens "Commit `abc1234` failed its health gate; `def5678` is still running."; blocked says why. When the sync itself deploys, the node's commit reads "by git sync for sam" and carries `Chadash-Source: <url>@<sha>`.

**Rules.**
- A synced stack's draft routes answer `409 synced`, "Edit `<path>` on `<branch>` in `<url>`."; the web shows the editor read-only.
- A rollback by a person holds the sync ("Held at `<commit>`…") until an admin resumes it. Resuming compares the tip afresh; a rejected commit stays rejected.
- Protected stacks can't follow git (`409 protected`).
- Turning on deploy on push, changing the remote and taking a sync over need a step-up (authz T3 on `stack.sync`). Taking over makes it the caller's. While a sync deploys on push (before or after the change), so does pointing it at other content: a new branch, path or credential (secret, kind, user or pinned host key), or a name added to `secrets_allow`. The step-up is asked before the node is.
- `rejected_sha` holds the last 20 commits that failed their health gate, newest first, separated by spaces, so its first word is still the latest. The API answers `rejected_sha` (the newest) and `rejected_shas`. They stay across a change of branch, path or remote, so moving away and back doesn't re-arm one.
- URLs are compared normalized: lowercase scheme and host, no default port, no trailing `/` or `.git`. Stored URLs keep their spelling.
- The credential is a vault entry, `stacksync/<id>/credential` (`{username, secret}`), read for one call and never answered. URLs with userinfo are refused.
- The saved credential goes only to the remote it was saved for (compared normalized). Testing another URL takes its own secret (`422` otherwise), and saving another URL with the secret left out is refused rather than carrying it to the new host. A token (`https` kind) is never sent over `http://`, in a test or a save.
- One Inbox item per sync (`stack.<node>.<stack>.sync`, class `problem`, scenario `stack-sync`): a rejected or blocked commit, a paused sync and a commit deploy on push asked about need attention; a held sync and a remote failing for more than 3 polls in a row are hints. The asked-about item closes when its approval is denied or expires (`Service.ApprovalEnded`, the engine's `OnApprovalEnded`), or when a deploy succeeds.

## Deploy webhooks

A hook (`hooks.go`) belongs to a remote (normalized) and a branch, never a stack. A valid request triggers every active sync on them, on any node, which then checks and fetches as on a poll. Only the body's `ref` is read: another branch answers `202 ignored`.

| Sender | Header checked | Signed over | Replay |
|---|---|---|---|
| `github` | `X-Hub-Signature-256: sha256=<hex>` | body | `X-GitHub-Delivery`, 24 h |
| `gitea` | `X-Gitea-Signature: <hex>` | body | `X-Gitea-Delivery`, 24 h |
| `forgejo` | `X-Forgejo-Signature: <hex>` | body | `X-Forgejo-Delivery`, 24 h |
| `chadash` | `X-Chadash-Signature: sha256=<hex>`, `X-Chadash-Timestamp` | `ts + "." + body` (`notify.Sign`) | timestamp within 5 minutes |
| `gitlab` | `X-Gitlab-Token` | nothing: a shared secret, `subtle.ConstantTimeCompare` | none |

- Each hook checks only its sender's header, with `hmac.Equal`, so a GitHub hook never accepts a GitLab token.
- The body is read raw through `http.MaxBytesReader` (1 MiB; `413` past it).
- An unknown hook and a bad signature answer the same `401 {"error":"unauthorized"}`.
- `httpx.Limiter` limits per client address (IPv6 per /64) first, and per hook once the hook is found, so made-up IDs don't fill the per-hook table.
- Refusals are counted per hook, and the first in each 10 minutes is audited as `stack.hook.refused` (reason only, never a header), by actor kind `anonymous` named `webhook`.
- The secret (`stackhook/<id>` in the vault, 256 bits as hex) is answered once, when the hook is made or rotated. Rotating it invalidates the old one at once. `/api/stacks/hooks` is never gzipped (BREACH; `internal/server`'s `noCompress`).
- Making a hook with `deploy_on_push` switches only the caller's own syncs on that remote and branch; the response lists the others as `not_switched`, and the web says so.

## Tests

`sync_test.go` plays the syncer through a table of states (a new tip, the same tip, a rejected tip, drift, held, an owner demoted or deleted, a newer tip during a pending approval, a secret outside the allowlist, a rollback by hand), with a fake remote in place of `stack.remote.check` and `stack.fetch` (the node's own tests fetch from a real repository). `hooks_test.go` has signature vectors for all five senders, the wrong secret, another sender's header, replay and a stale timestamp, one hook triggering syncs on two nodes and none on another branch, and `FuzzHookVerify`.

The tests run this package against the real node module, in process, over a `Caller` that marshals JSON and flattens errors to text as a transport would, on `stackstest.Docker`. Only the test binary links the node module.
