# Changelog

Everything that changes in Cha Dash, written for the people who run it.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Until 1.0, a new minor version (0.2, 0.3, …) may change the config, the API or how the image behaves; its notes will always say what to do. Each release's section here is also the text of its [GitHub release](https://github.com/ban-red/cha-dash/releases), so links in it are full URLs.

## [Unreleased]

### Added

- **A sheet for each container.** Choose a container in the containers tile or in ⌘K and its sheet opens over the page: why it's down, image and tag, uptime or exit code, health check, restarts, ports, networks, its compose project, engine and node, links and labels, plus what it depends on and what uses it. Its open problem is one line that opens the Inbox. The address (`?r=…`) opens it again after a reload, and Back closes it.
- **Act on a container from its sheet.** Its actions, by tier, with the usual confirmation, and the reason when one can't run. Remove can take the anonymous volumes too. Operators and up can read its last 50 log lines in the sheet. History lists what was done to it, and admins see its audit log there, with a link to Settings → Audit filtered to it. An Inbox item about a container links to its sheet.
- **Sheets for Docker engines and compose stacks.** An engine's sheet says what it reads through, its Docker version and its node, and lists its containers, problems first. A stack's sheet shows its folder and compose files and its containers, with a link to its editor in Settings → Stacks. The engines named under the containers tile open their sheet.
- **Settings → Nodes**: each chadash-node, whether it's connected, its version, address and key, and what its policy allows, in plain words. Revoke a node by typing its name. Regenerate the pairing token, and see the command that reads it on the hub's host.
- **Settings → Notifications**: Web Push to your browsers, ntfy, and signed webhooks. Each channel has a test button that sends one now and says how it went, and the delivery log shows every message, failures included.
- **Symptom-first defaults.** Something down is sent at once. Something that needs attention waits 5 minutes first, so a flap that clears by itself never pages anyone. An integration to reconnect and a stuck stack notify once each. Hints never notify, and neither do security findings at the Relaxed level. Resolutions are sent for things that were down.
- **Quiet by design.** One message per problem per change, also across restarts. Several at once, as on a backup night, arrive as one message. A burst past a channel's limit is dropped and logged, not queued.
- **Every critical (T4) action** notifies the admins and the owner, however it ended. This rule can't be turned off.
- **"Notify me on this device"**, for every role, in Settings → Notifications. The browser only asks for permission when you click it. A notification opens the incident in the Inbox. Household accounts hear only about what's on their boards.
- Failed sends are retried with backoff (30 s, 2 min, 10 min, 30 min), honouring `Retry-After`. Devices whose subscription expired are removed.
- Webhooks are versioned JSON with an `X-Chadash-Signature` HMAC-SHA256 header; the format is in `internal/notify/README.md`. Links in messages need `CHADASH_PUBLIC_URL`.
- **Settings → API tokens**: make tokens for scripts and automations. You pick what each may read (the Home Graph, the Inbox, the audit log) and run (every action your role allows, a family such as every Docker action, or single actions), and when it expires: 90 days by default, a year at most. The token is shown once, with Copy; Cha Dash keeps only a fingerprint. The list shows when and from where each token was last used. Revoke one and it stops working at once.
- A token acts as you and never has more than your role, even if your role changes later. The owner sees everyone's tokens and can revoke any; admins, everyone's but the owner's.
- Send it as `Authorization: Bearer cdt_…` to `/api/` routes. Tokens can't sign in, step up, or touch users, tokens or settings. New plain reads for scripts: `GET /api/graph` and `GET /api/inbox`.
- **Actions from a token:** up to T1 they run when the token approves its own plan. **T2 actions wait in the Inbox** for an admin or the owner, who sees who asked, the plan's steps and what it affects, then approves with the hold ring and a fresh step-up, or denies it. The token can poll the request and the task. Unanswered requests expire after an hour; revoking the token withdraws them. T3 and T4 never run from a token.
- Every step is in the audit log: the token's request, who approved or denied it, expiry, and the action itself, named after the token and its user.
- **"Let Cha Dash act"** in an integration's settings: the control token (Proxmox VE, Proxmox Backup Server) or Unraid's read-only/control choice now sits in its own folded section, apart from the credential that reads. It says whether the connection can act now, what saving would change, and, for Unraid, what each choice means. The recipe for the control token sits beside the form.
- **"Open its settings"** on an action that can't run yet goes straight to that section.
- **Where Cha Dash runs**, at the bottom of Settings → Security: the chain the hub runs on (its container, the Docker host, the VM, the host) and the router in front of it.
- **Cha Dash finds its own container.** Through chadash-node, the node names the hub's container; otherwise the hub reads its own container ID. Unraid relates the hub to its container too, when it can tell. Stopping, restarting, recreating or removing that container is then the critical confirmation, off unless you turn it on.
- **"Cha Dash itself"** leads the list in a confirmation whose blast radius includes the hub, with a warning that this page goes offline until it's back.
- **Tiles show service icons.** The hub fetches a tile's icon URL, or its link's favicon, checks it and serves it itself, so the strict CSP stays. Monograms remain the fallback, and for tiles that need you.
- **Backups carry the security settings and the "same thing?" decisions.** Exports include the security level, the step-up rule and the correlation decisions; imports show them field by field. Only the owner can import the security settings; anyone else imports the rest, and the preview says what was left out.
- **Settings → Stacks**: manage compose stacks on each node from the web. It lists status, drafts, drift and Cha Dash's own protected stack, and adopts running projects in place.
- A plain compose editor checks the file as you type: errors with line numbers, referenced secrets, a plan per service and a diff. Drafts save on the hub.
- Deploy and roll back go through the hold-to-confirm ring and show live progress. A deploy runs seven steps on the node: validate, plan, a dry-run preflight that blocks a bad tag before anything stops, commit, pull and up, a restart-aware health gate, then keep or roll back. A failed health check rolls back on its own, pinned to the image digests that last ran; if that fails too, the stack is stuck and the Inbox says so.
- Every change to a stack is a commit in the stacks root's git repository, so history and rollback are just git.
- Stack secrets are `${secret:name}` references, resolved in memory at deploy time and never written to disk or git. They're write-only: set or delete them, but values are never shown again.
- Stack actions are admin+, whatever their tier: stacks are settings.
- **Approve or decline Seerr requests** (also Overseerr and Jellyseerr). Pending requests show up as resources, so ⌘K finds them ("Movie request #42 by …") and lists Approve (one tap, with a toast) and Decline (hold to confirm, since Seerr tells the person who asked). Both check Seerr's answer afterwards and land in the audit log. A request someone already handled in Seerr says so instead.
- Tier 1 specs can now define actions: a target addressed by an ID checked against a pattern, a read that proves it exists, preconditions, one templated write and a check afterwards. Secrets never go into paths or bodies. The format stays internal until v1.0.
- **⌘K → Add widget** opens edit mode with the widget picker for the board on screen.
- **Undo a dismissal.** Dismissing discovered services in the Inbox shows a toast with Undo, and edit mode lists the dismissed ones ("Dismissed · N") to offer again.
- **Up next can include unmonitored releases.** Edit the calendar tile and tick "Include unmonitored releases"; they're marked as such. A Homepage calendar imported with `unmonitored` per source keeps it. Sonarr, Radarr and Lidarr now read unmonitored releases too, marked, and never in place of monitored ones.
- **Discovered containers become their catalog widget.** A container whose Homepage widget label or image is a known integration is suggested as that integration's widget (the Sonarr queue, Now playing, Home Assistant, or the service tile with its stat) when that integration is connected, and the Inbox says which to connect otherwise.
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
- **A new Demo Home.** The demo is now a fictional family home instead of a copy of the maintainer's lab: a router host, a three-node Proxmox cluster, a workstation, an Unraid NAS and two Docker hosts, with more of the catalog (Jellyfin, AdGuard Home, SABnzbd, Seerr, Paperless-ngx, Audiobookshelf, Nextcloud, Speedtest Tracker). Its day keeps the same kinds of incidents and adds a slow internet connection, Nextcloud stuck in maintenance mode, and AdGuard Home read over plain http. A board saved against the old demo shows empty tiles where it named old hosts; edit it, or start a dev hub over with `bun run dev --fresh`.
- **Demo mode says so.** A "Demo" tag sits beside the wordmark, a note under the Briefing explains that the page is a simulated lab on its own UTC clock, and the browser tab reads "Demo · Cha Dash". The note gives way to the offline and stale banners.
- **chadash-node**, a second image (`ghcr.io/ban-red/chadash-node`) that runs beside the hub in `compose.yaml` and holds this host's Docker socket. The hub never does. It dials the hub over the compose network and pairs on its own, with a secret the hub writes into a volume they share; nothing else can pair.
- **The node's local policy** (`/etc/chadash-node/policy.yaml`) decides what Cha Dash may do on the host: by default it reads, streams logs, starts, stops, restarts, pulls and recreates, but never removes or execs, and never touches Cha Dash itself. The hub can't widen it.
- **Docker reads through the node:** the Docker integration takes `node://local` as its endpoint, with the same containers, stacks and IDs as before. New installs find it in the first-run wizard.
- **Docker actions through the node:** start and stop (with undo), restart, recreate and pull (held to confirm, listing what depends on the container), remove (typed to confirm, saying which volumes stay), and the last 50 log lines. The node's policy has the last word, and the plan says when it refuses.
- **Adopting a project another tool deployed.** A running project whose compose labels name a path the node can't see (Hawser and others run compose inside their own container) can now be adopted from a copy of its file at `<stacks root>/<project>/compose.yaml`. Before, it showed "can't adopt".
- **Moving from Dockhand**: deploy/moving-from-dockhand.md moves stacks from Dockhand and Hawser one at a time, without restarting them.
- **chadash-node on other Docker hosts**, by hand until v0.3: `deploy/compose.node.yaml` runs the node alone, and it pairs once with the hub's pairing token (deploy/README.md, "Nodes on other hosts").
- **Compose `depends_on`** shows up as dependencies between containers, so a plan to restart a database lists the apps that use it.
- **`compose.yaml` runs chadash-node instead of the read-only socket proxy.** To keep the proxy and skip the node, start with `compose.socket-proxy.yaml` as well (deploy/README.md). An existing install keeps its Docker instance on `tcp://socket-proxy:2375` until you switch it to `node://local`.

### Fixed

- **Container logs show up.** Running Logs on a container read 50 lines and showed only "Done". The lines now open in a dialog, with Copy. Any other action that ends with log lines, such as a failed pull, offers Show log on its message.
- **Esc on "Confirm it's you"** closes only that dialog. Settings and the Inbox behind it stay open.
- **Hold-to-confirm works with screen readers, voice control and switch access.** A click that isn't a hold turns the button into a two-step **Confirm**, and the next press confirms. Holding still works as before.
- **Task progress is read out once**, when its step changes and when it ends, without percentages.
- **The "What this affects" list is readable at rest** instead of faded. In an approval it's never dimmed.
- **Keyboard focus stays put** in Settings and approvals: it moves into a confirm or form when it opens and back when it closes, and stays on a working button through the step-up. Settings keeps Tab inside the sheet, and a blocked Deploy can be reached and says why.
- **A user's role, the notification grace and the Security choices save with a Save button**, so moving through them with the arrow keys no longer saves or asks you to confirm it's you.
- The audit log says when older events fail to load, names each Details button after its event, and says how many events match a filter. A notification rule's checkbox goes back when its save fails. A stack secret's Set button is named "Set <name>".
- **Listing a resource's actions is faster and bounded**: at most 4 reads at a time and 3 seconds in all. An action whose state couldn't be read in time shows as unavailable with a reason instead of holding up ⌘K. Proxmox guests list in about 2 API calls instead of 20.
- **Task logs stream line by line** on the live connection instead of resending the whole log each time. The live log keeps 25 to 50 lines.
- **A tile's link is named for what it shows.** When a tile has no icon, the name of its link starts with the monogram on screen ("PH, open Photos"), so voice control can find it by what you see (WCAG 2.5.3, Label in Name).
- **The audit log records a cancel before the end it caused.** It could show a cancelled task failing before anyone had cancelled it.
- **A finished action always reads back as finished.** Just after a task ended, `GET /api/tasks/{id}` could briefly still report it as checking. A hub that stopped at that moment could later record a task that had succeeded as interrupted.
- **A spec action whose check runs out of time says what didn't happen**, in the spec's own words (for example "not moved"), instead of "didn't answer in time", when earlier checks had answered.
- **A webhook or ntfy address Cha Dash refuses fails once, with the reason**, instead of being retried four times on a host without IPv6.
- On 32-bit hubs (armv7), the Demo Home no longer refuses a script that repeats an incident two days later.

### Security

- **Open live connections now follow sign-outs and account changes.** Signing out, being disabled, a role change and a handover of ownership close that person's open tabs' live connections at once; the page reconnects as whoever it is now. Each connection also rechecks its session every minute (so an expired one ends) and reconnects after 12 hours.
- **Container logs are for operators and up.** Reading a container's logs (`docker.container.logs`) was open to viewers and household because it's T0; logs often hold secrets, so it now needs Operator or above. Household accounts run only actions marked for the household, at every tier. Log lines stay in the live task and are no longer saved with it, so they're gone after a restart.
- **More changes reach the audit log.** Integration connections (created, changed, deleted: names, the fields that changed and the URL's host, never a secret), passkeys added, renamed and removed, step-ups that worked and that failed, board layout saves and dismissed suggestions, "same thing?" decisions, the first-run wizard and its discovery, imports and exports (counts and IDs), and stack drafts. Refusals are recorded too: changing the owner's account, invalid users, a token scope your role can't hand out, revoking someone else's token. A change is recorded even when the browser hangs up mid-request.
- **Admins no longer see or revoke the owner's API tokens**, as they can't change the owner's account.
- **Two quick approvals can't share one T4 confirmation.** The step-up a critical action spends is now used up in the same step that starts it.
- **A token's actions name its user** in the audit log (`on_behalf_of`). Older entries keep their exact bytes and hashes.
- **Household accounts no longer get chadash-node's details or addresses** in the Home Graph (live, `GET /api/graph` and the first page load). Boards render as before.
- **Record the audit log's head.** `chadash audit verify`, `GET /api/audit/verify` and the audit export (`X-Chadash-Audit-Head`) now show the last event's number and hash. The chain can't protect itself from someone who can write the database file; keep the head somewhere else and check later exports still contain it (SECURITY.md).

- **A browser's push subscription can't be taken over** by another account: subscribing an endpoint someone else registered is refused. Turn notifications off in the first account to move a browser.
- **NAT64 and 6to4 addresses** are checked by the IPv4 address inside them, for Web Push and the icon fetcher.
- **SVG icons are parsed as XML and checked against an allowlist** of elements and attributes, so encoded `javascript:`, animation tricks and external references are refused.
- **Links are http(s) only**: a tile or widget link with any other scheme (`javascript:`, `data:`) renders without a link. A notification click can only open a page on the hub.
- **The icon fetcher has its own rules** (SECURITY.md): only the URLs your tiles and connections name, never loopback, link-local or cloud metadata addresses (checked after DNS), one redirect on the same host, short timeouts, 256 KiB, PNG, ICO, WebP or SVG only, and SVGs with scripts or external references refused. Icons are served with a sandbox CSP.
- **The node's pairing secret is single-use.** Pairing spends it and the hub writes a fresh one into the `pairing` volume, so a copy of it is worth nothing once the node has paired. The hub also writes a fresh one when a node is revoked and at start once a node has paired, and an admin can regenerate it (`POST /api/nodes/pairing/regenerate`, audited). Nothing to do: a fresh install still pairs by itself, and the node reads the new secret from the file when it needs it.
- **Pairing never replaces an enrolled node.** Pairing under the name of a live node is refused. To replace a node whose volume was lost, revoke the old one first. Pairing is accepted only from private and loopback addresses; `CHADASH_NODE_PAIRING_FROM` changes that.
- **`stacks.deploy` can't reach the node's own keys.** A stack can no longer bind the Docker socket, `/`, the node's directories or volumes, or the `pairing` volume, take another container's volumes, or read env files outside its directory; `include` and `extends` from other files are refused. Privileged containers, host namespaces, devices, `cap_add` and host paths still work, but the editor and the deploy confirmation now say they're root on that host, and the node's policy can deny them (`stacks.deny`, `stacks.bind_roots`). Deploying a stack is root on the node's host by design; deploy/README.md says how to lock it down.
- **Protected containers are protected from stacks too.** A deploy, plan or rollback that would recreate or remove a container `docker.protect` names is refused. The hub's container is protected whatever its service is called, and a node that couldn't tell its own compose project at start asks again.
- **Rollbacks send only the stack's own secrets.** A rollback sends only secrets an earlier deploy sent to that stack on that node; validation takes secret names from the hub's own reading of the draft. A node can't ask for another stack's secrets.
- **Smaller fixes.** A node can't announce another node's key (or the hub's) as its next one. Unauthenticated `/node` connections are cheaper to hold: the hello is read under 64 KiB, frames are buffered as they arrive, handshakes are capped per address and overall, unknown keys count against the rate limit, and their refusals log once a minute. Integrations reach only the node verbs they use, by name. Environment redaction covers `Config.Env` only; command lines and labels come back as they are (now documented).
- **Sign-in rate limits are harder to get around.** IPv6 clients are limited per /64 rather than per address. A flood of new addresses no longer resets everyone's budget. And each account allows 20 wrong passwords, then one a minute, wherever they come from; passkeys aren't affected.
- **Settings → Security warns about a reverse proxy Cha Dash doesn't trust**, and about a sign-in over plain HTTP when `CHADASH_PUBLIC_URL` is https. Both are logged once, too. Until the proxy is in `CHADASH_TRUSTED_PROXIES`, every sign-in shares one rate limit and the session cookie isn't marked Secure.
- **An instance with certificate checks off keeps saying so** in Settings → Integrations, not only when you test it.
- **The node channel is mutual TLS 1.3 inside the WebSocket**, each side known by a pinned Ed25519 key, with keepalive pings, size limits and version negotiation. Pairing checks the hub's key before the node sends its secret. Nodes can be revoked (`POST /api/nodes/{id}/revoke`), and keys rotate over the live channel.
- **Built with Go 1.27.3 and golang.org/x/net 0.61.** Together they fix 14 vulnerabilities in code Cha Dash uses: compromised certificate intermediates are distrusted, HTTP/1 and HTTP/2 connections can't be desynchronised or exhausted the ways now known, Range headers are bounded, and an `html/template` escaping bug is fixed.
- **The node writes its own files under the stacks root through `os.Root`**, so neither a `..` nor a symlink can take a write outside it.
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
