# Spike 4.6 · node sandbox

[`sandbox.sh`](sandbox.sh) creates a throwaway Docker LXC for testing `chadash-node`, covering compose deploy → diff → health gate → rollback, without touching a production host.

## Which host: `mars-2`

| Host | Verdict | Why |
|---|---|---|
| **mars-2** | ✅ **use this** | Idle cluster member (0 guests, ~1% CPU, ~28% memory). It's in the same cluster as `pvedocker`, so it's a realistic "Docker in an LXC" setup, and it runs nothing anyone depends on. |
| mars-1 | ok, second choice | Hosts `pvedocker` (Traefik, Uptime Kuma, Linkding, Karakeep, Homepage). A runaway test could starve them |
| pluto | ✗ never | Runs OPNsense, your router |
| minig5 | ✗ | Home Assistant and Frigate |
| jupiter-nas | ✗ | Already at ~95% memory |
| io | ✗ | Your workstation and gaming VMs |

## Run it

The sandbox always uses **CT ID 3696**, the same as Cha Dash's port, so it's easy to spot and never steals the next free ID. Before creating anything, the script checks that 3696 is free across the whole cluster and that the bridge exists.

```bash
scp spikes/node-sandbox/sandbox.sh root@mars-2.lan:/root/
```

```bash
ssh root@mars-2.lan '/root/sandbox.sh create'
```

The script:
1. Picks the newest Debian 13 standard template, falling back to Debian 12.
2. Creates an **unprivileged** CT with nesting and keyctl enabled (what Docker needs inside an LXC). It's tagged `chadash;sandbox`, has autostart off, and gets 2 cores, 2 GB RAM and a 16 GB disk on the first active rootdir storage.
3. Gets an address via DHCP on `vmbr0`, and copies the PVE host root's `authorized_keys` into the CT. That file usually holds the cluster's keys, not your Mac's, so see [SSH](#ssh-permission-denied-publickey) below.
4. Installs Docker Engine and the Compose plugin from Docker's official apt repo.
5. Starts a sample compose project, `hello`, at `/opt/stacks/hello` (nginx with a healthcheck, port 8080), so the node has something to detect and adopt.

When it finishes it prints the CT ID, the architecture and the IP. Put `NODE_SANDBOX_HOST=<ip>` in your local `.env.local`.

To override the defaults, set environment variables, e.g. `STORAGE=local-zfs VLAN=20 MEMORY=4096 ./sandbox.sh create`. The header of `sandbox.sh` lists them all.

Other commands:

```bash
ssh root@mars-2.lan '/root/sandbox.sh status'
```

```bash
ssh -t root@mars-2.lan '/root/sandbox.sh destroy'
```

`destroy` refuses to touch any CT that isn't tagged `sandbox`, and makes you type the hostname. It's the T3 safety ladder in miniature.

## What runs in it

The node prototype lives in [`../node-compose`](../node-compose/README.md): adopt `hello` in place → diff → deploy → health gate → automatic rollback, across six scripted scenarios. It's cross-compiled on your Mac (`GOOS=linux GOARCH=amd64` for mars-2; the script prints the right value) and copied over. Builds only run when you say so.

## Troubleshooting

### Start failed with `sync_wait: 34 … Failed to spawn container`

The real cause was in the debug log: `Exec format error - Failed to exec "/sbin/init"`. Earlier versions of this script picked the **arm64** template on an **amd64** host, because the template index lists every architecture and the selection sorted by name. The script now only picks templates built for the host's architecture. Clean up and re-run:

```bash
scp spikes/node-sandbox/sandbox.sh root@mars-2.lan:/root/
```

```bash
ssh -t root@mars-2.lan '/root/sandbox.sh destroy && /root/sandbox.sh create'
```

`destroy` finds the leftover sandbox by its hostname and tag, and asks you to type the hostname. The wrong-architecture template is still cached on the host (about 123 MB); remove it with `pveam remove local:vztmpl/debian-13-standard_13.6-1_arm64.tar.zst`.

If the start fails again, the script re-runs it with `--debug` and prints only the ERROR and WARN lines, with a hint for the likely cause (architecture, bridge, AppArmor, keyctl or UID mapping). Send me that block.

### SSH: `Permission denied (publickey)`

The CT only trusts the keys that were in the PVE host's `/root/.ssh/authorized_keys`. If you log in to the PVE host with a password, your key isn't among them. Add it through the host (one password prompt):

```bash
ssh root@mars-2.lan "pct exec 3696 -- sh -c 'cat >> /root/.ssh/authorized_keys'" < ~/.ssh/id_ed25519.pub
```

### Other issues

- **`perl: warning: Setting locale failed` during provisioning:** harmless. SSH forwarded your Mac's `LANG`, which the minimal template doesn't have. The script now provisions with `C.UTF-8`.
- **No IPv4 after 60 s:** check DHCP on that bridge or VLAN (`BRIDGE=…`, `VLAN=…`).
- **Containers fail to start with an AppArmor or sysctl "permission denied" error:** this is a known class of issue with Docker inside unprivileged LXCs, depending on the containerd/runc and PVE versions. Try these in order:
  1. Update both the PVE host and the CT.
  2. For this **sandbox only**, add `lxc.apparmor.profile: unconfined` to `/etc/pve/lxc/<vmid>.conf` and restart the CT. Don't do this on a production container.
- **`STORAGE` auto-detect picked the wrong pool:** pass `STORAGE=<name>` explicitly. `pvesm status --content rootdir` lists candidates.
