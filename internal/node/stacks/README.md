# internal/node/stacks

The node's stacks module. Cha Dash owns compose stacks on the host the node runs on ([ADR-0006](../../../docs/adr/0006-owned-stacks-and-thin-node.md)) and deploys them with the Compose SDK and go-git ([ADR-0020](../../../docs/adr/0020-node-deploys-with-compose-sdk-and-go-git.md)). Only `chadash-node` links this package. The hub talks to it through the verbs in [`internal/stackproto`](../../stackproto).

## Layout

- The stacks root defaults to `/opt/stacks`. It uses Dockge's layout: `<root>/<stack>/compose.yaml` and an optional `.env`. `compose.yml`, `docker-compose.yaml` and `docker-compose.yml` work too.
- The root is a git repository, created on first use with a `.gitignore` that keeps `.env` files out. Every change Cha Dash makes is a commit.
- A stack adopted in place **outside** the root is listed in `.chadash/stacks.json`. A shadow copy of its file at `.chadash/shadow/<stack>/<file>` is what git versions. The real file stays where it is, and a deploy writes it there.
- Commits carry trailers the module reads back: `Chadash-Kind` (deploy, rollback, adopt, unadopt, accept), `Chadash-Stack`, `Chadash-Actor`, and on deploys `Chadash-Previous-Image: <service>=<repo>@sha256:…` for each image that was running when the deploy started.
- Files are staged by writing the git index directly. `Worktree.Add` would walk the whole worktree, and a stack directory often holds its data too.

## Verbs

| Verb | Policy | What it does |
|---|---|---|
| `stack.list` | `stacks.read` | Managed stacks: status from Docker, drift, protected, last commit |
| `stack.detect` | `stacks.read` | Running compose projects that aren't managed yet (found by their labels), plus root directories nobody adopted. Each says whether it can be adopted, and why not |
| `stack.get` | `stacks.read` | One stack: the committed file (or the file at commit `at`), `.env` key names only, `x-chadash`, containers, the last 50 commits, referenced secrets |
| `stack.validate` | `stacks.read` | Loads a proposed file with compose-go. Secret references are checked for syntax, never resolved. Returns messages with line numbers |
| `stack.plan` | `stacks.read` | Compares config hashes (`compose.ServiceHash`) with the running containers' labels. Lists services to create, recreate (and pull) or remove, and a downtime estimate |
| `stack.deploy` | `stacks.deploy` | The seven steps below |
| `stack.rollback` | `stacks.deploy` | An earlier commit's file as a new commit, pinned to the digests it last ran, through the same steps |
| `stack.adopt` | `stacks.adopt` | Adopt in place: commit a root directory, or register an external path and start its shadow history. Nothing moves and nothing restarts. On a drifted stack it accepts the edits made outside Cha Dash |
| `stack.unadopt` | `stacks.adopt` | Stop versioning a stack. Its files and containers stay as they are |

Errors are `stackproto.Error{code, message}`, and `Error()` reads `<code>: <message>`. `stackproto.ErrorFrom` gets the code back even if a transport passes only the text. Blocked, rolled back and stuck are outcomes in `DeployResult`, not errors.

## The deploy pipeline

1. **Validate.** The loader, secret references, `x-chadash`. Every reference must come with the request.
2. **Plan** from config hashes against the running containers. A file equal to the last commit is `unchanged`, and nothing happens.
3. **Preflight.** A dry run through Compose's reconciler resolves every image, so a bad tag is `blocked` before anything stops. A registry the node can't reach reads "couldn't check", not "would fail".
4. **Commit** the attempt (`Deploy media: recreate web · by sam via web · t_…`), recording the images running now.
5. **Pull** what changed, then **up --wait**. Progress streams as `stackproto.Progress`.
6. **Health gate.** A soak (default 20 s; `x-chadash.soak`) fails on any exit, restart or unhealthy state, or on `restarting`. `--wait` alone passed a crash loop in spike 4.6.
7. **Keep it, or roll back.** The previous file comes back as a new commit, with each image pinned to the digest it last ran (only the image scalar changes, so comments stay). It goes through the same gate with a shorter soak (default 5 s; `x-chadash.rollback_soak`). If that fails too, the outcome is `stuck`. A brand-new stack that fails is taken down again and leaves the repo; the hub keeps its draft.

Other rules:
- One operation per stack at a time (`busy`).
- `expect_head` refuses a deploy planned against an older commit (`conflict`).
- A file on disk that differs from its last commit refuses a deploy (`drift`) until the edits are accepted with `stack.adopt`.

## Secrets

- A file references a secret as `${secret:name}`. The hub reads the values from its vault (`stacks/<name>`) and sends them with plan, deploy and rollback requests.
- The module substitutes them **inside each parsed string value**, never in the YAML text, so a value can't inject YAML. A `$` in a value is escaped, so Compose doesn't interpolate it again. `$${secret:x}` is a literal.
- Values reach Docker's container config, as with any compose environment, and nothing else. They're never in the stacks root, git, progress, results or errors: messages are scrubbed of every value of 4+ characters. `TestSecretsNeverTouchDisk` scans every file under the root, every git object and every progress message.
- `stackproto.Secrets` prints as `[N secrets redacted]` under `fmt` and `slog`.
- The node's own environment is left out of interpolation. Only the stack's `.env` is used, so a stack renders the same wherever the node runs.

## Protected stacks

Cha Dash's own stack can be viewed and diffed, not deployed (`protected`), until self-update lands in v0.3. A stack is protected when its name is in `Options.Protected` (the node should pass its own compose project), or when a running container or the file uses an image starting with `ghcr.io/ban-red/chadash`.

## x-chadash

```yaml
x-chadash:
  soak: 20s           # every container must stay up this long after a deploy
  rollback_soak: 5s   # the same after an automatic rollback
  wait: 2m            # how long up --wait may take
  description: Media server
  tags: [media]
  household: false
```

Unknown keys and bad values are warnings, never errors.

## Tests

`go test` needs no daemon. `stackstest.Docker` is a fake that behaves by image name (`…unhealthy…`, `…crashloop…`, `…exits…`, `…missing…`) and stamps Compose's real config hashes. The tests cover:
- the validator, safe secret substitution, and the hash planner, including foreign hashes meaning "recreate everything once";
- digest pinning, and history and rollback commits;
- blocked, stuck, and a new stack that fails;
- drift, conflict, protected, busy, and a missing secret;
- adopt in place (root and external), un-adopt, and accepting edits;
- secrets never on disk.

**The Done-when scenario** is `TestBadDeployRollsBackOnItsOwn`. v1 is healthy. v2 fails its healthcheck, or crash-loops. The gate catches it, and v1 comes back as a new commit pinned to its digest, comments kept. The failed attempt stays in the history, and a good deploy works afterwards. `internal/stacks`'s `TestScenarioBadDeployRollsBackOnItsOwn` runs the same scenario end to end through the action engine.

## What needs a real daemon

`sdk.go` (the `Docker` implementation over the Compose SDK) is compiled but not exercised by `go test`. The maintainer's live run should confirm:
- hash parity between `compose.ServiceHash` on a project loaded from memory and the labels the CLI stamped (spike 4.6 confirmed it for path-loaded projects);
- the dry run blocking a missing tag, and the "couldn't check" classification of registry errors;
- `RepoDigests` giving pullable pins, and a pinned rollback pulling by digest;
- restart counts during the soak, and `--wait` timeouts;
- `docker compose ls` still showing node-deployed stacks at their real paths;
- the node container's view of external paths (they must be mounted at the same path).

## Running it for real

For the maintainer, on a scratch stack on the hub's host only (see 08 · v0.2 → Dogfooding writes). Needs a go-ahead per run.

1. Run `chadash-node` with the stacks root mounted, e.g. `/opt/stacks:/opt/stacks`, and paired with the hub.
2. Settings → Stacks → New stack `scratch`:
   ```yaml
   services:
     web:
       image: nginx:1.27-alpine
       healthcheck: { test: ["CMD", "wget", "-qO-", "http://localhost"], interval: 3s, retries: 2 }
   ```
   Save the draft, then Deploy (hold). Expect the task to succeed and history to show `Deploy scratch: create web`.
3. Edit the healthcheck to probe a port nothing listens on (`http://localhost:9`). Save, then Deploy.
4. Expect the task to fail with code `rolled_back`, "…rolled back on its own…". History shows `Roll back scratch: container scratch-web-1 is unhealthy (pinned web)` above the failed `Deploy`. `/opt/stacks/scratch/compose.yaml` should be v1 with `nginx:1.27-alpine@sha256:…`, and `docker compose ls` should still list `scratch`.
5. Optional: a missing tag (`nginx:9.99-nope`) should be blocked before anything stops, with web's uptime unchanged.
6. Clean up: un-adopt or `docker compose down` the scratch stack.
