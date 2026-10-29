# The Cha Dash beta

Thank you for trying Cha Dash. This is **0.2.0-beta.1**, the first published beta: one calm, live page for your homelab that can now also act on it, with a plan, the confirmation each action's risk needs, and an audit log. We're looking for people who will run it every day for a while and tell us honestly how it goes. What's in it is in the [changelog](../CHANGELOG.md).

## Install it in 3 minutes

You need Docker with the Compose plugin, on amd64 or arm64 (armv7 works on a best-effort basis), and 512 MB of RAM or more. On a Raspberry Pi 3 or newer, use a 64-bit OS.

```sh
mkdir chadash && cd chadash
curl -fsSLO https://raw.githubusercontent.com/ban-red/cha-dash/main/deploy/compose.yaml
# There's no "latest" until 0.1.0, so follow the beta tag:
printf 'services:\n  chadash:\n    image: ghcr.io/ban-red/chadash:beta\n  chadash-node:\n    image: ghcr.io/ban-red/chadash-node:beta\n' > compose.override.yaml
docker compose up -d
docker compose logs chadash | grep setup
```

1. Open `http://<this host>:3696` and enter the setup token from the last line (`setup_token=cds_…`).
2. Create your account. Cha Dash then offers to add a passkey: skip it for now on plain HTTP, and add one later in Settings → Security once you have HTTPS.
3. **Connect your home**: import your Homepage config, let it look at the machines you name, or add services one by one. Each form shows how to make a key that can only read.

That's the whole install: no YAML to write, and no settings required. Running behind a reverse proxy, keeping the vault key elsewhere, backups and the rest are in [deploy/README.md](../deploy/README.md).

- **On Unraid?** Use the [Unraid template](../deploy/unraid/README.md).
- **Just looking?** Demo mode runs a made-up homelab with scripted incidents and connects to nothing. Fetch [`compose.demo.yaml`](../deploy/compose.demo.yaml) next to the others, then:
  ```sh
  docker compose -f compose.yaml -f compose.override.yaml -f compose.demo.yaml up -d
  docker compose -f compose.yaml -f compose.override.yaml -f compose.demo.yaml logs chadash | grep setup
  ```
  (With `-f`, Compose doesn't load `compose.override.yaml` by itself, so it's named.) It's a separate project with its own data, but it uses port 3696 too, so stop a real install first.

## What "read-only" means

- **Cha Dash v0.1 can't change anything.** It doesn't start, stop, restart, update or delete anything, on any platform. Actions arrive in v0.2, each with a confirmation that fits its risk, an audit log, and undo where there can be one.
- **The Docker socket proxy** in `compose.yaml` allows GET requests only, and only Cha Dash can reach it. The hub itself never touches the Docker socket.
- **Your credentials** should only be able to read, too. Each integration's recipe shows how, and **Test connection** warns when a key could write. A few services (Home Assistant, Plex, the *arr apps and some others) don't offer a read-only key at all; Cha Dash still only reads with them.
- **The only things Cha Dash writes** are its own database and key in `/data`, and temporary files in a small in-memory `/tmp`.

## What we'd love to hear about

Anything at all, but especially:

- **The 3 minutes.** Did you get from `docker compose up -d` to a live page in under 3 minutes, without writing YAML? Where did you slow down?
- **Your Homepage config.** Did it come across? What didn't map, and what did you expect it to become?
- **Your services.** Do the numbers match what the service itself says? Which versions do you run? Did a Repair explain clearly what to fix?
- **One picture.** Did Cha Dash merge things that aren't the same, or miss things that are?
- **The page itself.** Is it calm when all is well? Is the briefing right? Does *Needs attention* show what actually needs you?
- **Phones.** Installing it to a home screen, and how it behaves when the hub is out of reach.
- **Accessibility.** Keyboard use, screen readers, and contrast in the theme you use.
- **Small machines.** How it runs on a Pi or another small board: `docker stats` numbers help.
- **Design.** Anything that looks or feels off.

## How to tell us

Open an issue on [GitHub](https://github.com/ban-red/cha-dash/issues/new/choose) with the template that fits:

- **[Bug report](https://github.com/ban-red/cha-dash/issues/new?template=bug_report.yml)** for something that doesn't work the way it should. It asks for your version (`docker compose exec chadash /chadash version` prints it), how you installed it, and the logs (`docker compose logs chadash`). More detail: set `CHADASH_LOG_LEVEL: debug` in `compose.override.yaml` and restart.
- **[Integration request](https://github.com/ban-red/cha-dash/issues/new?template=integration_request.yml)** for a service Cha Dash should read.
- **[Design feedback](https://github.com/ban-red/cha-dash/issues/new?template=design_feedback.yml)** for anything that looks or feels off. Screenshots are very welcome.
- **A blank issue** for ideas, questions and everything else.

**Issues are public.** Before you paste logs, config or screenshots, take out tokens, passwords, domain names and public IP addresses.

**Found a security problem?** Please don't open an issue. Report it privately, as [SECURITY.md](../SECURITY.md) describes.

## Privacy

**There's no telemetry.** No analytics, no crash reports, no update checks, no usage statistics. Cha Dash never contacts us.

**The hub connects only to these**, and we checked the code for this release to be sure the list is complete:

1. **The services you add**, at the addresses you give, with the credentials you give. Its requests say `User-Agent: chadash/<version> (+https://dash.cha.sh)`.
2. **plex.tv**, only while you use **Sign in with Plex**. Cha Dash creates a sign-in PIN there and checks it until you approve, sending a random ID for your hub and the name "Cha Dash". The token comes back to your form and goes into the vault. After that, Plex is read from your own server.
3. **Open-Meteo** (`api.open-meteo.com`), only if you add the **Weather** integration. It sends the coordinates you set, rounded to about 100 m, every 15 minutes while someone is looking and every 30 otherwise. There's no key and no account, and you can point it at a self-hosted Open-Meteo instead.
4. **Your own network, in the setup wizard's *Discover* step**: the socket proxy from `compose.yaml` as soon as the step opens, and the machines and Docker addresses you list there. It sends plain GET requests to the usual ports of the services Cha Dash knows, without credentials. It never scans a network.

**The web app loads nothing from anywhere else.** Fonts and icons are built in, and the page's Content-Security-Policy (`default-src 'self'`) makes the browser refuse anything from another origin.

**Your secrets stay on your server.** They're encrypted at rest, never sent to the browser, written as placeholders in exports (or sealed with your passphrase), and left out of diagnostics.

Pulling the image or downloading a binary is an ordinary download from GitHub, the same as for any other software you get there.

## What isn't there yet

- **No actions.** That's v0.2.
- **One account**, the owner. Roles arrive with actions.
- **Not built yet:** discovered containers becoming catalog widgets (they become service tiles), "Add widget" from ⌘K, undoing a dismissed suggestion, your merge decisions in exports, the calendar's "unmonitored" option, and the wall and e-ink levels of detail.
- **Icons from other sites** don't show on tiles, because the browser's security policy blocks them; tiles show a monogram until the hub can keep icons itself.
- **Passkeys and installing the app need HTTPS**, and passkeys need `CHADASH_PUBLIC_URL` ([how](../deploy/README.md#behind-a-reverse-proxy)).
- **armv7 is best-effort.** It's built and tested as 32-bit in CI, but not on real armv7 hardware.

## Updating between betas

```sh
docker compose pull
docker compose up -d
```

[Back up](../deploy/README.md#backing-up) first: a new version may migrate the database, and there's no going back without the backup. Each beta moves the `beta` tag; to stay on one, pin it in `compose.override.yaml`, for example `ghcr.io/ban-red/chadash:0.2.0-beta.1` and `ghcr.io/ban-red/chadash-node:0.2.0-beta.1`.

**Checking the image** is optional. Every release is signed by its GitHub workflow, and [cosign](https://docs.sigstore.dev/) v3 or later can confirm it:

```sh
cosign verify ghcr.io/ban-red/chadash:beta \
  --certificate-identity-regexp '^https://github\.com/ban-red/cha-dash/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## Leaving

`docker compose down` stops it and keeps your data. `docker compose down -v` also deletes the data, including the key to your stored credentials, so take a backup first if you might come back. Either way, we'd love to hear why: a short issue helps a lot.
