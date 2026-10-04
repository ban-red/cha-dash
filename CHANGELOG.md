# Changelog

Everything that changes in Cha Dash, written for the people who run it.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Until 1.0, a new minor version (0.2, 0.3, …) may change the config, the API or how the image behaves; its notes will always say what to do. Each release's section here is also the text of its [GitHub release](https://github.com/ban-red/cha-dash/releases), so links in it are full URLs.

## [Unreleased]

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
