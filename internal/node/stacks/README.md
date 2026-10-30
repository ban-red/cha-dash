# internal/node/stacks

The node's stacks module. Cha Dash owns compose stacks on the host the node runs on ([ADR-0006](../../../docs/adr/0006-owned-stacks-and-thin-node.md)) and deploys them with the Compose SDK and go-git ([ADR-0020](../../../docs/adr/0020-node-deploys-with-compose-sdk-and-go-git.md)). Only `chadash-node` links this package. The hub talks to it through the verbs in [`internal/stackproto`](../../stackproto).

## Layout

- The stacks root defaults to `/opt/stacks`. It uses Dockge's layout: `<root>/<stack>/compose.yaml` and an optional `.env`. `compose.yml`, `docker-compose.yaml` and `docker-compose.yml` work too.
- The root is a git repository, created on first use with a `.gitignore` that keeps `.env` files out. Every change Cha Dash makes is a commit.
- A stack adopted in place **outside** the root is listed in `.chadash/stacks.json`. A shadow copy of its file at `.chadash/shadow/<stack>/<file>` is what git versions. The real file stays where it is, and a deploy writes it there.
- Commits carry trailers the module reads back: `Chadash-Kind` (deploy, rollback, adopt, unadopt, accept, self-update), `Chadash-Stack`, `Chadash-Actor`, on deploys `Chadash-Previous-Image: <service>=<repo>@sha256:…` for each image that was running when the deploy started, and on a move `Chadash-Moved-From: <the old file>`. A deploy a git sync started also carries `Chadash-Source: <url>@<sha>`, and its subject reads "by git sync for sam".
- Files are staged by writing the git index directly. `Worktree.Add` would walk the whole worktree, and a stack directory often holds its data too.

## Verbs

| Verb | Policy | What it does |
|---|---|---|
| `stack.list` | `stacks.read` | Managed stacks: status from Docker, drift, protected, last commit |
| `stack.detect` | `stacks.read` | Running compose projects that aren't managed yet (found by their labels), plus root directories nobody adopted. Each says whether it can be adopted, and why not. A running project with a directory of its name in the root is offered with that directory's file, whatever its labels say: that's how a project another tool deployed from inside its container is brought over |
| `stack.get` | `stacks.read` | One stack: the committed file (or the file at commit `at`), `.env` key names only, `x-chadash`, containers, the last 50 commits, referenced secrets |
| `stack.validate` | `stacks.read` | Loads a proposed file with compose-go. Secret references are checked for syntax, never resolved. The guard's refusals are errors and its root-equivalent findings warnings (see "What a stack may do on the host"). Returns messages with line numbers |
| `stack.plan` | `stacks.read` | Compares config hashes (`compose.ServiceHash`) with the running containers' labels. Lists services to create, recreate (and pull) or remove, a downtime estimate, and `root_equivalent`: what the file does that amounts to root on the host. Refuses what the guard refuses (`invalid`) and recreating or removing a container `docker.protect` names (`protected`) |
| `stack.deploy` | `stacks.deploy` | The seven steps below |
| `stack.rollback` | `stacks.deploy` | An earlier commit's file as a new commit, through the same steps. The services it recreates are pinned to the digests they last ran; the rest are left as they are |
| `stack.adopt` | `stacks.adopt` | Adopt in place: commit a root directory, or register an external path and start its shadow history. Nothing moves and nothing restarts. On a drifted stack it accepts the edits made outside Cha Dash |
| `stack.unadopt` | `stacks.adopt` | Stop versioning a stack. Its files and containers stay as they are |
| `stack.remote.check` | `stacks.sync` | A branch's tip on a git remote, listed without fetching. Over SSH it also returns the host key the server presented, for the hub to pin |
| `stack.fetch` | `stacks.sync` | A shallow fetch of the branch into the node's cache, then one file at the tip: its content, the commit, its subject and author. Over SSH only the pinned host key is accepted |
| `stack.move` | `stacks.adopt` | Bring a project into the root, keeping its name (see "Moving into the root"). `dry_run` answers the same and leaves nothing behind |
| `stack.selfupdate` | `stacks.selfupdate` | Update the hub's container to a tag (see "Cha Dash updates itself"). Off by default |
| `stack.selfupdate.result` | `stacks.selfupdate` | The last self-update (`running` while one runs), whether this node can update the hub, and the hub's image now |

Errors are `stackproto.Error{code, message}`, and `Error()` reads `<code>: <message>`. `stackproto.ErrorFrom` gets the code back even if a transport passes only the text. Blocked, rolled back and stuck are outcomes in `DeployResult`, not errors.

## Moving into the root

`stack.move {stack, from_path | content, env?}` (`move.go`) copies a project's file into `<root>/<stack>/compose.yaml`, and its `.env` beside it, keeping the project name so volumes and networks stay.

- **From the node** (`from_path`): the file must be the one Docker's labels name for the running project (one file, readable, outside the node's own directories, as for adopting), or the registered file of a stack adopted in place. A path the hub makes up is refused. The `.env` beside it comes along.
- **From the hub** (`content`, `env`): an importer's copy, validated like any draft.
- **Relative paths** become absolute against the old file's directory, each listed with its line: short and long bind sources (`./data` → `/srv/app/data`), `env_file`, `build` and `build.context`, and top-level `configs` and `secrets` files. Only those scalars change; quoting, comments and the rest stay. An `env_file` naming the `.env` that moves along stays relative. With supplied content the old directory isn't known, so a relative path refuses the move and names its line. An `env_file` made absolute is outside the stack's directory, which the guard refuses, so such a move is refused too.
- **Nothing runs.** The files are staged in the new directory, validated, guarded and planned against the running containers, then committed as `Chadash-Kind: adopt` with `Chadash-Moved-From`. An in-place registration and its shadow copy go. A refusal or a dry run removes the staged directory again. The plan usually recreates everything once, because the containers' working-directory label changes.
- **Refused:** a stack already managed in the root, an existing `<root>/<stack>` (anything at all), a protected project, what the guard refuses, and an invalid file.
- `stack.detect` says `moveable`, or `move_refusal`. `stack.get` on a moved stack says `moved_from` while the old file is still there.

## Cha Dash updates itself

`stack.selfupdate {image, ref}` (`selfupdate.go`) runs only on a node in the hub's compose project (`Options.SelfProject`) that can recreate containers (`Options.SelfUpdate`, the node's Docker module). The hub is the one container in that project whose image is `ghcr.io/ban-red/chadash`; `chadash-node` is never touched.

1. `image` is a tag, or `ghcr.io/ban-red/chadash:<tag>`. Another repository or a digest is `invalid`.
2. The result is written to `.chadash/selfupdate.json` as `running`, with the hub's task ID, before anything changes.
3. Pull the tag and pin its digest (`…:0.3.0@sha256:…`). A failed pull is `blocked`. The digest the hub already runs is `unchanged`.
4. Recreate the hub's container from its own config with the pinned image (`RecreateImage`; a failure puts the old container back: `blocked`).
5. Gate: the hub must report healthy within 120 s. Exited, restarting or unhealthy fails at once.
6. On failure, recreate it with the previous digest and gate again: `rolled_back`, or `stuck`.
7. Commit `.chadash/selfupdate.json` with `Chadash-Kind: self-update` under the project's name, so it shows in that stack's history, then write the final result.

The work runs on even when the request's context ends: the hub that asked is the one going away. `stack.selfupdate.result` reads the file. A `running` record that no update holds means the node stopped mid-update, and it reads as `stuck` with a sentence. The policy grant is off by default. `deploy/compose.yaml` turns it on for the node beside the hub with `CHADASH_NODE_SELFUPDATE`.

## The deploy pipeline

1. **Validate.** The loader, secret references, `x-chadash`, the guard. Every reference must come with the request. The guard runs again on the file loaded with the real secret values, since a value can be a path.
2. **Plan** from config hashes against the running containers. A file equal to the last commit is `unchanged`, and nothing happens. A plan that recreates or removes a protected container (`docker.protect`, the hub, the node) is refused (`protected`).
3. **Preflight.** A dry run through Compose's reconciler resolves every image, so a bad tag is `blocked` before anything stops. A registry the node can't reach reads "couldn't check", not "would fail".
4. **Commit** the attempt (`Deploy media: recreate web · by sam via web · t_…`), recording the images running now.
5. **Pull** what changed, then **up --wait**. Progress streams as `stackproto.Progress`.
6. **Health gate.** A soak (default 20 s; `x-chadash.soak`) fails on any exit, restart or unhealthy state, or on `restarting`. `--wait` alone passed a crash loop in spike 4.6.
7. **Keep it, or roll back.** The previous file comes back as a new commit, with the image of each service it recreates pinned to the digest it last ran (only the image scalar changes, so comments stay). A service the attempt didn't touch still runs that file, so it isn't pinned or recreated. It goes through the same gate with a shorter soak (default 5 s; `x-chadash.rollback_soak`). If that fails too, the outcome is `stuck`. A brand-new stack that fails is taken down again and leaves the repo; the hub keeps its draft.

Other rules:
- One operation per stack at a time (`busy`).
- `expect_head` refuses a deploy planned against an older commit (`conflict`).
- A file on disk that differs from its last commit refuses a deploy (`drift`) until the edits are accepted with `stack.adopt`.

## What a stack may do on the host

`stacks.deploy` runs the compose files the hub sends, and a compose file can mount the host's disk, run privileged or share the host's namespaces. **An admin who can deploy is root on the node's host, by design** (stacks need bind mounts for media libraries and `cap_add` for VPN containers). The guard (`guard.go`) holds back what would hand a stack the node's own keys:

- **Always refused**, whatever the policy says: binding the Docker socket (`/var/run/docker.sock`, `/run/docker.sock`, `CHADASH_NODE_DOCKER_HOST`'s path), `/`, the node's directory (`/etc/chadash-node`, its identity) or the pairing volume (`/pairing`), or any path that contains one of them; the node's own volumes (by name, read from its container's mounts) and their host paths; local-driver volumes and file secrets or configs on those paths; `volumes_from` another container; `env_file` and `label_file` outside the stack's directory; `include`; `extends` from another file. The references the loader would read are checked in a first pass that reads no other file, so a refused `env_file` is never opened.
- **Denied by the policy, or flagged:** `privileged` (and `security_opt` that turns confinement off), `host_namespaces` (`network_mode`, `pid`, `ipc`, `uts`, `userns_mode` or `cgroup` set to `host` or another container), `devices` (also `device_cgroup_rules`, `gpus`), `cap_add`, and `host_binds` (host paths outside the stack's directory, or outside `stacks.bind_roots` when `host_binds` is denied). Nothing is denied by default; validation warns about each and the plan lists them as `root_equivalent`, which the hub shows at the top of the deploy confirmation.
- **Adopting** reads a running project's compose file only if it's an absolute `.yaml` or `.yml` path outside the node's own directories: anything that can start a container can set the labels that name it.

To lock a node down, list features in `stacks.deny` (with `bind_roots` for the paths stacks may use), or remove `deploy` from `stacks.allow`.

## Git remotes

A stack that follows git ([ADR-0021](../../../docs/adr/0021-git-sync-and-deploy-webhooks.md)) reads one file from a remote. The remote is an input, never history to merge: the stacks root only changes when the hub deploys that content, like any deploy.

- **The cache** is in the node's own directory (`<CHADASH_NODE_DIR>/git-cache`), never under the stacks root. There's one bare, shallow repository per remote and branch, named by a hash of the normalized URL (lowercase scheme and host, no default port, no trailing `/` or `.git`) and the branch, so stacks that follow the same remote share it. A cache that fails to fetch is thrown away and fetched again once; if that fails too, the cache is removed. Caches no fetch used for 14 days are removed when the node starts, and at most hourly after a fetch.
- **Limits.** 60 s per call, 1 MiB per file and 64 MiB written per fetch (`Options.FetchTimeout`, `Options.MaxFile`, `Options.MaxFetch`). A fetch past 64 MiB is stopped (`invalid`) and its cache removed; the cap counts the pack go-git writes, so it holds over https, ssh and file alike. Only depth 1 of one branch is fetched, without tags.
- **Redirects.** Over HTTP, only the first request may redirect (go-git's default). With `stacks.remotes` set, the target must be in it (`forbidden` otherwise). A redirect never leaves https, and the credential is dropped when the host or port changes.
- **What it reaches.** `https://`, `http://` and `ssh://` only (`file://` is for tests). A URL with a user or password in it, or scp-style `git@host:path`, is refused (`invalid`): the user goes with the credential. The policy's `stacks.remotes` limits the hosts (`forbidden` outside it); an entry without a port matches any port. All of this is checked before any I/O.
- **Credentials.** The hub sends them with each call: none, an HTTPS user and token (the user defaults to `git`), or an SSH private key (the SSH user defaults to `git`). They stay in memory and are never logged; only the host is. Every error is scrubbed of the secret and of any `user:password@` a transport echoes.
- **SSH host keys.** `stack.remote.check` accepts the key the server presents and returns it (`ssh-ed25519 AAAA…`). `stack.fetch` requires a pinned key and accepts only that one; another key fails with a sentence saying it changed.

## Secrets

- A file references a secret as `${secret:name}`. The hub reads the values from its vault (`stacks/<name>`) and sends them with plan, deploy and rollback requests.
- The module substitutes them **inside each parsed string value**, never in the YAML text, so a value can't inject YAML. A `$` in a value is escaped, so Compose doesn't interpolate it again. `$${secret:x}` is a literal.
- Values reach Docker's container config, as with any compose environment, and nothing else. They're never in the stacks root, git, progress, results or errors: messages are scrubbed of every value of 4+ characters. `TestSecretsNeverTouchDisk` scans every file under the root, every git object and every progress message.
- `stackproto.Secrets` prints as `[N secrets redacted]` under `fmt` and `slog`.
- The node's own environment is left out of interpolation. Only the stack's `.env` is used, so a stack renders the same wherever the node runs.

## Protected stacks

Cha Dash's own stack can be viewed and diffed, not deployed (`protected`): `stack.selfupdate` changes the hub's image instead. A stack is protected when its name is in `Options.Protected` or is the node's own compose project (`Options.SelfProject`, asked again at most once a minute until Docker answers), or when a running container or the file uses an image starting with `ghcr.io/ban-red/chadash`. That holds for a running project the node can't adopt too, such as the hub's own with a `compose.override.yaml`: `stack.detect` lists it with `protected` and the reason, and `stack.get` returns a read-only view of it (its containers, no file) instead of `not_found`, so the hub takes no draft for it. Separately, `docker.protect` holds for stack verbs: a deploy, plan or rollback that would recreate or remove a protected container is refused (`Options.ProtectContainer`, the node's `Policy.Protected`).

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
- the guard: every always-refused path and reference (and that a refused `env_file` is never read), each policy denial, root-equivalent warnings in validation and the plan, a secret whose value is a private path, `docker.protect` on deploy, plan and rollback, the node's own project learned late, and adoption refusing the node's files;
- adopt in place (root and external), un-adopt, and accepting edits;
- git remotes: a fetch from a local repository over `file://` (go-git's in-process server), a new commit, a shared cache, the size, byte and time limits, pruning idle caches, redirects outside the allowlist, the URL and allowlist refusals, scrubbed errors, the auth each credential kind builds and SSH host-key pinning, and the `Chadash-Source` trailer;
- secrets never on disk;
- moving from another tool's file and from an in-place stack, supplied content, every rewrite, and each refusal (`move_test.go`);
- self-update: a good image, a bad one rolled back, the node's own image and other refusals, the policy grant, and the result while it runs and after the node stopped mid-update (`selfupdate_test.go`).

**The Done-when scenario** is `TestBadDeployRollsBackOnItsOwn`. v1 is healthy. v2 fails its healthcheck, or crash-loops. The gate catches it, and v1 comes back as a new commit pinned to its digest, comments kept. The failed attempt stays in the history, and a good deploy works afterwards. `internal/stacks`'s `TestScenarioBadDeployRollsBackOnItsOwn` runs the same scenario end to end through the action engine.

## What needs a real daemon

`sdk.go` (the `Docker` implementation over the Compose SDK) is compiled but not exercised by `go test`. The maintainer's live run should confirm:
- hash parity between `compose.ServiceHash` on a project loaded from memory and the labels the CLI stamped (spike 4.6 confirmed it for path-loaded projects);
- the dry run blocking a missing tag, and the "couldn't check" classification of registry errors;
- `RepoDigests` giving pullable pins, and a pinned rollback pulling by digest;
- restart counts during the soak, and `--wait` timeouts;
- `docker compose ls` still showing node-deployed stacks at their real paths;
- the node container's view of external paths (they must be mounted at the same path);
- self-update through the node's Docker module (`PullImage`, `RecreateImage`): the pulled tag's `RepoDigests`, the hub recreated from its own config with its labels, networks and volumes, the healthcheck seen by the gate, and the rollback to the previous digest. The hub's compose labels stay, so `docker compose ps` still lists it; a later `docker compose up -d` puts the file's tag back;
- a move of a project another tool started from a host path, and its first deploy from the root.

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
