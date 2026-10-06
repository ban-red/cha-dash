# Changelog

Everything that changes in Cha Dash, written for the people who run it.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Until 1.0, a new minor version (0.2, 0.3, …) may change the config, the API or how the image behaves; its notes will always say what to do. Each release's section here is also the text of its [GitHub release](https://github.com/ban-red/cha-dash/releases), so links in it are full URLs.

## [Unreleased]

### Added

- **Seven more integrations, all read-only**, each tested against responses written from its API's documentation:
  - **Nextcloud:** users and how many are active, files, shares and free space, with a serverinfo token that can read nothing else;
  - **Transmission:** speeds and how many torrents are seeding, downloading and queued, on the Downloads tile beside qBittorrent;
  - **NZBGet:** speed, queue and what's left, on the Downloads tile beside SABnzbd, with its restricted user;
  - **Lidarr:** the queue, what's wanted, and this week's album releases in Up next;
  - **Audiobookshelf:** books, podcasts and their hours, and who's listening;
  - **Paperless-ngx:** documents and the inbox, with a view-only user;
  - **Speedtest Tracker:** the latest download, upload and ping, with a token that can only read results.
- **Overseerr and Jellyseerr** connect as Seerr, their successor with the same API, and the add list says so.
- **Unraid can be read-only or controllable, with one key.** The form asks "Read-only, or let Cha Dash control Unraid?", and the recipe's consent link asks Unraid for the matching scopes. Controllable instances start and stop containers and VMs, and update containers, each with a plan, a check afterwards and an audit record. Test connection compares the key's grants with the choice, and an action whose scope the key lacks says which one and how to add it. Read-only instances have no actions.
- **Unraid VMs** show up, with their state, beside the containers.
- **App actions:** pause and resume all torrents in qBittorrent; search for missing episodes, movies or albums in Sonarr, Radarr and Lidarr; run the Home Assistant scripts you list in the instance's settings, and nothing else; pause Pi-hole or AdGuard Home for 5 minutes, which the Household role may do too. Each checks the result through the next read.
- **The Homepage importer** brings these over too: `overseerr`, `jellyseerr`, `nextcloud`, `transmission`, `nzbget`, `lidarr` (calendar included), `audiobookshelf`, `paperlessngx` and `speedtest` widgets.
- **Proxmox VE actions:** start, shut down, reboot and hard-stop a guest, take, roll back and delete snapshots, and back a guest up now, with live progress from Proxmox's task log. Each runs as a plan you confirm at its tier (undo for start, shutdown and new snapshots; hold for reboot and hard stop; the typed name for rollback and delete) and lands in the audit log.
- **Cha Dash knows when an action would take itself or the network down.** Rebooting, stopping or rolling back the guest the hub runs in, or the router's guest, or the host under either, needs the critical confirmation, which is off unless you turn it on. Cha Dash finds its own guest by its addresses, or by the new "Cha Dash's own guest" setting.
- **Proxmox Backup Server: prune a datastore per policy**, by its prune job's retention, from the "filling up" incident, the fix the demo shows.
- **Control tokens.** Proxmox VE and Proxmox Backup Server keep reading with their read-only token; a second, optional control token lets Cha Dash act. Each recipe has a control variant that grants only what the actions need. Without one, the actions show as unavailable with a link to it, and Test connection mentions it.
- **Incidents say whether their fix can run now**, and why not, so a button never offers what Cha Dash can't do.
- **Cancel a running task** from the task presence at the bottom left, or from the incident it's working on, when the action allows it. Cancelling is recorded in the audit log like any action.
- **⌘K runs actions.** Search for a host, guest or service (or open ⌘K on a focused tile), and the command bar lists what you can do to it, with the same confirmation as everywhere else. Actions Cha Dash can't run yet stay listed with the reason.

### Changed

- **Every role gets the instant first paint**, not only the owner.
- **Household accounts see only what their boards need.** The live inbox they receive holds problems about what's on their boards, with a title and a status, and no facts, causes or blast radius.
- **Roles below admin no longer see broken screens.** Settings opens on your own passkeys, and the setup guide, integrations, repairs' Reconnect and the "merge?" questions are left to admins and the owner.
- **The Demo Home's fixes run only through the action engine**, with the same plans, confirmations, tasks and audit records as a real home. The old demo-only endpoint is gone.
- **chadash-node**, a second image (`ghcr.io/ban-red/chadash-node`) that runs beside the hub in `compose.yaml` and holds this host's Docker socket. The hub never does. It dials the hub over the compose network and pairs on its own, with a secret the hub writes into a volume they share; nothing else can pair.
- **The node's local policy** (`/etc/chadash-node/policy.yaml`) decides what Cha Dash may do on the host: by default it reads, streams logs, starts, stops, restarts, pulls and recreates, but never removes or execs, and never touches Cha Dash itself. The hub can't widen it.
- **Docker reads through the node:** the Docker integration takes `node://local` as its endpoint, with the same containers, stacks and IDs as before. New installs find it in the first-run wizard.
- **Docker actions through the node:** start and stop (with undo), restart, recreate and pull (held to confirm, listing what depends on the container), remove (typed to confirm, saying which volumes stay), and the last 50 log lines. The node's policy has the last word, and the plan says when it refuses.
- **Compose `depends_on`** shows up as dependencies between containers, so a plan to restart a database lists the apps that use it.

### Changed

- **`compose.yaml` runs chadash-node instead of the read-only socket proxy.** To keep the proxy and skip the node, start with `compose.socket-proxy.yaml` as well (deploy/README.md). An existing install keeps its Docker instance on `tcp://socket-proxy:2375` until you switch it to `node://local`.

### Security

- **Sign-in rate limits are harder to get around.** IPv6 clients are limited per /64 rather than per address. A flood of new addresses no longer resets everyone's budget. And each account allows 20 wrong passwords, then one a minute, wherever they come from; passkeys aren't affected.
- **Settings → Security warns about a reverse proxy Cha Dash doesn't trust**, and about a sign-in over plain HTTP when `CHADASH_PUBLIC_URL` is https. Both are logged once, too. Until the proxy is in `CHADASH_TRUSTED_PROXIES`, every sign-in shares one rate limit and the session cookie isn't marked Secure.
- **An instance with certificate checks off keeps saying so** in Settings → Integrations, not only when you test it.
- **The node channel is mutual TLS 1.3 inside the WebSocket**, each side known by a pinned Ed25519 key, with keepalive pings, size limits and version negotiation. Pairing checks the hub's key before the node sends its secret. Nodes can be revoked (`POST /api/nodes/{id}/revoke`), and keys rotate over the live channel.
- **The recipe for Docker on another host** explains that the proxy serves container inspects, environment variables included, to anything connecting from Cha Dash's address. It suggests publishing on one interface or using a VPN, and shows how to turn inspects off.

## [0.1.0-beta.1] - 2026-10-04

The first beta of Cha Dash: a calm, live, read-only front page for your homelab. It reads from the services you already run and never changes anything in them. If you'd like to try it and tell us what you think, the [beta guide](https://github.com/ban-red/cha-dash/blob/main/docs/beta.md) has everything you need, and thank you.

### Added

- **The front page.** Boards and sections you arrange by dragging, in place or in edit mode, with layout history and undo. A one-line briefing says how the home is doing, and a **Needs attention** section appears on its own when something does. The phone layout is worked out for you.
- **Widgets for the v0.1 catalog**, each designed for its sizes and its states (loading, empty, healthy, attention, down, stale, needs repair): service tile, fleet card, host card, guest list, container list, Unraid array, storage bar, camera hero, Home Assistant summary, media calendar, now playing, download queue, uptime check, metric stat and sparkline, bookmarks, clock and weather.
- **26 integrations, plus weather**, all read-only, each tested against recorded or documented API responses:
  - infrastructure: Proxmox VE (several instances and clusters), Proxmox Backup Server, Unraid (API 4.37.4 or later), Docker (through a read-only socket proxy; containers with Homepage's `homepage.*` labels are offered as tiles), Traefik, OPNsense, Uptime Kuma, and built-in HTTP and TCP checks;
  - home: Home Assistant and Frigate;
  - media and downloads: Plex (with Sign in with Plex), Jellyfin, Sonarr, Radarr, Prowlarr, qBittorrent, SABnzbd, Tautulli, Bazarr and Seerr;
  - network: Pi-hole (v6) and AdGuard Home;
  - apps: Immich, Mealie, Linkding and Karakeep;
  - weather from Open-Meteo, with no key and no account.
- **One picture of your home.** When several sources see the same thing (a container that both Docker and Unraid report), it shows once. Cha Dash links what runs where, what routes to what and what monitors what, explains a failed Proxmox backup by the backup server being down, and asks before merging anything it isn't sure about.
- **The Inbox.** Problems, repairs (an integration that needs a new key, with the fix), and discoveries, such as a labelled container that could become a tile, or two things that might be one.
- **⌘K** to search and open anything on the page.
- **Themes:** Instrument Dark and Light, OLED, Night, High Contrast and Paper.
- **The Homepage importer.** Paste your Homepage `services.yaml`, `docker.yaml` and `settings.yaml`, see the plan, and apply it: a board, the integrations, and your secrets moved into Cha Dash's encrypted vault. Run it again and nothing changes twice.
- **Onboarding.** First run goes setup token, owner account, then an offer to add a passkey. **Connect your home** then lets you import, discover (only the machines and Docker endpoints you name; it never scans a network), or add services one by one.
- **Least-privilege recipes.** Every integration's form shows how to make a credential that can only read, with commands to copy and a `pveum` generator for Proxmox. **Test connection** reads back what the credential can actually do, and says so if it could write.
- **Passkeys.** Sign in with a passkey, no username needed, and manage them under Settings → Security. They need HTTPS (or `localhost`) and `CHADASH_PUBLIC_URL`.
- **Export and import.** Settings → Backup, or `chadash export` and `chadash import`: your boards, integrations and settings as one YAML file, with secrets as placeholders or sealed with a passphrase. Importing shows a field-by-field diff first, and never deletes anything.
- **A first paint with real values.** The page arrives with your last-known state already in it, then stays live over a single WebSocket.
- **An installable app that works offline.** Install it to your home screen (over HTTPS), and it shows the last-known state when the hub can't be reached.
- **Accessibility.** WCAG 2.2 AA is a release requirement: automated axe checks cover every view and every widget state, and every theme's text meets 4.5:1 contrast.
- **Demo mode.** The Demo Home simulator, a made-up homelab with scripted incidents, for trying Cha Dash without connecting anything (`compose.demo.yaml`).
- **Ways to run it.** Docker Compose, with zero required settings and a hardened container (non-root, read-only filesystem, no capabilities) next to an optional read-only Docker socket proxy. Images for linux/amd64 and linux/arm64, plus linux/arm/v7 as best-effort, signed with cosign and carrying build provenance and an SBOM. A Community Applications template for Unraid. Static binaries for Linux, and macOS builds for development.
- **Light to run.** One self-contained binary with the web app inside, and gzip compression for the app, the first paint and the API: the Demo Home's first page goes from 145 kB to 24 kB.

[Unreleased]: https://github.com/ban-red/cha-dash/compare/v0.1.0-beta.1...HEAD
[0.1.0-beta.1]: https://github.com/ban-red/cha-dash/releases/tag/v0.1.0-beta.1
