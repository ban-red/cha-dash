# Moving from Dockhand

This guide moves compose stacks from Dockhand and its agent, [Hawser](https://github.com/Finsys/hawser), to Cha Dash, one Docker host and one stack at a time. Containers keep running throughout. Volumes, networks and data stay where they are, because the compose project names don't change.

It was written against Dockhand with Hawser 0.2 in standard mode. Edge mode and other versions should work the same way, but check the labels in [step 1](#1-take-stock).

## How the two differ

| | Dockhand with Hawser | Cha Dash |
|---|---|---|
| The agent | Hawser. In standard mode it **listens** (port 2376) and passes the Docker API through, behind a token | chadash-node. It **dials** the hub, listens on nothing, and offers named verbs, never the Docker API. A local policy decides what Cha Dash may do |
| Where compose files live | On **Dockhand's** host, one folder per environment. Hawser runs compose from a copy | On the **Docker host itself**, in the node's stacks root (default `/opt/stacks/<stack>/compose.yaml`), which is a git repository |
| History and rollback | — | Every change is a commit. A deploy that fails its health gate rolls back on its own |
| Secrets | In the compose file or the stack's environment | `${secret:name}` references, resolved from the vault at deploy time and never written to disk |

## What Cha Dash doesn't do yet

Keep Dockhand around, read-only, for these until Cha Dash has them:
- A shell in a container. Exec is planned for v0.3.
- Live, streaming logs. Cha Dash shows the last 50 lines.
- CPU and memory per container.
- Browsing images, volumes and networks, pruning, and vulnerability scans.
- **"Pull and redeploy" for a whole stack.** Deploying an unchanged file does nothing. To update a stack whose images use `:latest`, Pull, then Recreate each container, or pin tags and change them in the file.

Dockhand's own settings don't come along: update schedules, registries and templates. Check them in Dockhand before you start. Private registries aren't supported yet: Cha Dash pulls without credentials.

## Before you start

- **A Cha Dash hub** that every Docker host can reach on port 3696 ([Install](README.md#install)).
- **A backup of each Docker host** you'll move: a VM or LXC backup, or at least its stacks' data directories.
- **One writer at a time.** From step 3 on, don't deploy a host's stacks from Dockhand. Two tools writing the same project will undo each other.

## 1 · Take stock

**In Dockhand,** for each environment:
- **Stacks** lists every stack. Its *Location* column is where Dockhand keeps the file, on Dockhand's host.
- Note which stacks are running, stopped or only created, and which have environment variables set in Dockhand rather than in the file.

**On each Docker host,** see what Docker's labels say about each project:

```sh
docker ps -a --format '{{.Label "com.docker.compose.project"}}  {{.Label "com.docker.compose.project.config_files"}}' | sort -u
```

Stacks Hawser deployed usually name a path **inside Hawser's container**, such as `/data/stacks/<stack>/compose.yaml`, or `-` when the file came in on standard input. Neither exists on the host. That's why the next steps copy the files over instead of adopting from the labels.

## 2 · Run a node on each Docker host

- **On the hub's host,** `compose.yaml` already runs one ([The node](README.md#the-node)).
- **On every other host,** use [`compose.node.yaml`](compose.node.yaml) ([Nodes on other hosts](README.md#nodes-on-other-hosts)).

**Choose the stacks root.** The default is `/opt/stacks`. If your data already lives in one folder per stack (`/srv/stacks/<stack>/…`), you can make that folder the root instead:
- Set `stacks.root` in the [policy](README.md#the-node).
- Mount the folder at the same path in the node's container.
- Binds inside a stack's own folder then aren't flagged as root-equivalent.

The node only commits compose files, never your data.

Then add Docker in Settings → Integrations with the endpoint `node://<host>`. Its containers should match `docker ps -a`.

Hawser can keep running. Nothing reads from both.

## 3 · Bring each stack over

> **Adopt, never "New stack", for a project that's already running.** If a brand-new stack's first deploy fails, Cha Dash takes it down again, and here that would take down your running containers. An adopted stack whose deploy fails rolls back to the adopted version instead.

For each stack, starting with the ones you'd lose least by restarting:

1. **Copy the file** from Dockhand's *Location*, or from its editor, to `<stacks root>/<stack>/compose.yaml` on the Docker host. **The folder name must be the stack's name in Dockhand**, which is also its compose project name.
2. **Take secrets out of it.**
   - Replace each password, token or key written in the file with a reference, such as `DB_PASSWORD: ${secret:app_db_password}`.
   - Set each value under Stack secrets in Settings → Stacks.
   - Do this **before** adopting: adopting commits the file, and git keeps every version.
   - If Dockhand held environment variables for the stack, put them in the file the same way, or in a `.env` next to it. `.env` files stay out of git.
3. **Adopt it.** In Settings → Stacks, the project is under *Running here, not managed yet*, with your copied file. Choose Adopt. Nothing restarts.
4. **Check the plan.** Open the stack: the editor checks the file and plans it against what's running.
   - "No changes" means the file matches what's running.
   - If every service would be recreated, the containers were started by a different compose version than the node's. Its first deploy recreates each one once. Choose a quiet moment.
   - Root-equivalent findings (privileged, devices, `cap_add`, host paths) are listed at the top. They were always there; now you see them.
5. **Deploy once.** This commits a known-good version with its image digests, so later rollbacks have something to go back to.

Stacks that bind the Docker socket can't be managed: the node refuses them, since a stack with the socket controls the node. Hawser's own stack is one.

## 4 · Cut over

When every stack on a host is adopted and has deployed through Cha Dash at least once:
1. Remove the environment from Dockhand. Its copies of the files stay on Dockhand's host as a backup.
2. Stop Hawser on that host (`docker compose down` in its folder). That also closes port 2376, which passed the whole Docker API through.

Move Dockhand's own host last, through the hub's node if they share a host. Then retire Dockhand.

## Going back

Unadopt each stack: run the `stack.unadopt` action on it (`POST /api/actions/plan`, then approve; Settings has no button for it yet). Its files and containers stay as they are. Then stop the node, start Hawser again, and Dockhand carries on from its own copies.

## When something doesn't look right

| What you see | Why, and what to do |
|---|---|
| The project says "can't adopt" and names a path like `/data/stacks/…` | The node hasn't found your copy. Check the folder is `<stacks root>/<project name>/` and the file is `compose.yaml` (or `compose.yml`, `docker-compose.yaml`, `docker-compose.yml`) |
| The plan recreates every service | The containers' config hashes came from another compose version. Their first deploy recreates them once |
| `protected` when planning | The stack would recreate a container the node's policy protects (`docker.protect`), or Cha Dash itself |
| A deploy is refused with `drift` | The file was edited on disk after adopting. Choose "Accept edits made outside Cha Dash" in the editor, or put the file back |
| Validation refuses a bind | The guard keeps stacks off the node's own files and the Docker socket. Other host paths are allowed, and flagged |
