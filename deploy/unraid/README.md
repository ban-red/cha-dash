# Cha Dash on Unraid (`deploy/unraid/`)

[`chadash.xml`](chadash.xml) is a Community Applications template for the Cha Dash hub. It's the same image as the [Compose install](../README.md), set up the Unraid way: Unraid manages the container, and nothing is converted to Compose ([ADR-0010](../../docs/adr/0010-unraid-stays-unraid.md)).

## Install it before it's listed in Community Applications

Until the template is in CA, add it as a user template. In the Unraid terminal (**>_** in the top bar):

1. **Give the container its appdata folder.** The hub runs as uid 65532, not as root, so it can't write to a folder Docker creates for it:
   ```sh
   mkdir -p /mnt/user/appdata/chadash
   chown 65532:65532 /mnt/user/appdata/chadash
   chmod 700 /mnt/user/appdata/chadash
   ```
2. **Fetch the template** into Unraid's user templates:
   ```sh
   curl -fsSL -o /boot/config/plugins/dockerMan/templates-user/my-chadash.xml \
     https://raw.githubusercontent.com/ban-red/cha-dash/main/deploy/unraid/chadash.xml
   ```
3. **Docker → Add Container**, and pick **chadash** from the Template list (under User templates). Check the port and the appdata path, then **Apply**.
4. **Open the Web UI** (port 3696 unless you changed it) and create your account. The username starts as `admin`; pick a password. Do it straight away: until the owner exists, anyone who can reach the hub can create it.

Then **Connect your home**. For this server, add the **Unraid** integration with an API key: Unraid's own API reads the array, the shares and the containers Unraid manages, without waking spun-down disks. It needs the Unraid API 4.37.4 or later, which is built into Unraid 7.2 and later and comes with the Unraid Connect plugin before that. The form shows the exact key to create.

**Upgrades:** Docker tab → **Check for Updates**. Back up the appdata folder first: the database and `secret.key` belong together ([Backing up](../README.md#backing-up)).

## What the template sets

| Setting | Value | Why |
|---|---|---|
| Repository | `ghcr.io/ban-red/chadash:beta` | The beta. It moves to `latest` when 0.1.0 is released |
| Port | 3696 → 3696 | Change the host side only ([ADR-0009](../../docs/adr/0009-default-port-3696.md)) |
| `/data` | `/mnt/user/appdata/chadash` | The SQLite database (in WAL mode) and the vault key. Like other SQLite apps, it's happiest on a pool: an appdata share that lives only on your cache pool, or a direct path such as `/mnt/cache/appdata/chadash` |
| `CHADASH_PUBLIC_URL` | empty | Optional. The `https://` address you open Cha Dash at. Passkeys need it, and HTTPS ([more](../README.md#behind-a-reverse-proxy)) |
| `CHADASH_TRUSTED_PROXIES` | empty | Optional. Your reverse proxy's address or subnet, so sign-in limits and logs see real clients and cookies are marked `Secure` |
| Extra parameters | `--read-only --tmpfs /tmp:size=64m,mode=1777 --cap-drop ALL --security-opt no-new-privileges:true --user 65532:65532` | The hardening `compose.yaml` uses: a read-only root filesystem, a small in-memory `/tmp` for SQLite, no Linux capabilities, no privilege escalation, and a non-root user (the image's own, stated explicitly) |

**About the template format.** Unraid passes *Extra Parameters* to `docker create` as written, so any `docker run` flag is allowed there; the template schema has no dedicated fields for these. *Privileged* stays off.

**No shell.** The image is just the binary on `scratch`, so Unraid's **Console** has nothing to open. Use **Logs**. The health check is built into the binary, so Docker reports the container's health without one.

## Docker access (the socket-proxy note)

**Don't mount `/var/run/docker.sock` into Cha Dash.** The template doesn't, and the hub never needs it: access to the socket is root on the server.

**On Unraid you usually need no Docker access at all.** The Unraid integration already sees the containers Unraid manages, their state and their autostart order.

If you also want the **Docker** integration, for containers Unraid doesn't manage or for `chadash.*` label discovery, give it a read-only socket proxy, the way `compose.socket-proxy.yaml` does (chadash-node on Unraid arrives in v0.3):

- run [`wollomatic/socket-proxy`](../README.md#docker-on-another-host) with the same GET-only allowlist, and don't publish its port;
- put it and chadash on one custom Docker network (`docker network create chadash`, then set both containers' *Network Type* to it), and allow only `-allowfrom=chadash`;
- add Docker in Cha Dash with the endpoint `tcp://socket-proxy:2375`. **Test connection** says what kind of endpoint it found.

[Docker access](../README.md#docker-access) explains what the allowlist permits and what it refuses.

## For the maintainer: listing it in CA

- The template's `TemplateURL`, `Icon` and `ReadMe` point at `main` in this repo. The icon is the PWA's for now; a dedicated one can replace it at the same kind of URL.
- Switch `Repository` to `ghcr.io/ban-red/chadash:latest` and drop `Status:Beta` from `Category` when 0.1.0 ships.
- Once it's submitted, the repo slug can't change again ([decision 8](../../docs/plan/04-roadmap.md#decisions)).
