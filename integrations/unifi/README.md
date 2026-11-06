# UniFi Network (`unifi`)

Reads a UniFi Network Application, read only: its adopted devices (gateways, switches, access points) with state, uptime, firmware and whether an update is waiting; the site's client counts; and the WAN's health, with an incident while it's down. It works with a UniFi OS console (Dream Machine, Cloud Gateway, Cloud Key Gen2 and later, UniFi OS Server) and with the Network Application installed on its own.

**Tier:** silver (repairs, version, grants read back, fixture tests). **Capabilities:** `net.devices`, `net.clients`, `net.wan`. **Secrets:** `password`.

**Not tested against a real console.** The maintainer's lab has no UniFi. The fixtures in [`testdata/integrations/unifi/`](../../testdata/integrations/unifi/README.md) are written by hand from the classic API's documented shapes for Network Application 9.0.114 on UniFi OS 4.1.13, and `live:verify` can't check them yet.

## Why a session login, not the API key

Network Application 9.0 added an official API with keys (`X-API-KEY`, under `/proxy/network/integration/v1/` on UniFi OS). It was the first choice, and it doesn't cover this integration's job:

- **No WAN health.** It lists WAN interfaces (as definitions, for firewall and routing rules), but nothing says whether the WAN or the internet is up. That's half of what this integration is for.
- **More requests.** Firmware (`firmwareUpdatable`) is on each device's detail and uptime on each device's `statistics/latest`, so a site of *n* devices costs 2*n*+2 requests a sync instead of two.
- **UniFi OS only.** The standalone Network Application has no API keys.

The classic API gives all of it in two reads (`stat/device`, `stat/health`), on both platforms, since long before 9.0. It needs a session: a local account with the **View Only** role (see the recipe), which can't change anything. When the key API reports WAN health, this integration should move to it; the resource IDs don't depend on the API.

## Config

```json
{"url": "https://10.0.40.1", "tls": {"mode": "tofu"}, "platform": "unifi_os", "site": "default",
 "username": "chadash", "password": "${secret:…/password}"}
```

| Key | Meaning |
|---|---|
| `url` | The console's address. A standalone controller is usually `https://host:8443` |
| `tls` | `{"mode": "verify" \| "pin" \| "tofu" \| "insecure", "pin": "<sha256>"}`. Consoles serve a self-signed certificate, so `tofu` or `pin` (the framework's TLS policy, as for Proxmox); `insecure` would hand the password to anything that answers |
| `platform` | `unifi_os` (default) or `standalone`. It picks the login (`/api/auth/login` or `/api/login`) and the API's root (`/proxy/network` or none) |
| `site` | The site's short name, default `default`; for other sites the code in the UI's address, `/manage/<site>/…` |
| `username`, `password` | A local account. The password goes to the vault and only into the login's body |

## What it reads

| Request | When |
|---|---|
| `POST /api/auth/login` (UniFi OS) or `/api/login` (standalone) | Once, then again only when UniFi ends the session (a `401`) |
| `GET …/api/s/<site>/stat/device` | Every sync |
| `GET …/api/s/<site>/stat/health` | Every sync |
| `GET …/api/s/<site>/stat/sysinfo` | The first sync, then hourly: the version |
| `GET …/api/self/sites` | Test connection: the site and the account's role on it |
| `POST /api/auth/logout` or `/api/logout` | When the instance stops, with the CSRF token from the login |

`…` is `/proxy/network` on UniFi OS. Every 30 s while watched and every minute otherwise, so a WAN outage reaches the inbox within a minute. Both reads are small whoever's watching, so the interest doesn't change what's read.

## What it publishes

| Resource | Kind | Attrs | Health |
|---|---|---|---|
| `unifi:<instance id>/site` | `network`, named after the instance | `platform: unifi`, `unifi_platform`, `site`, `version` (the Network Application's), `devices`, `devices_offline`, `upgradable`, `pending_adoption`; caps `net.devices`, `net.clients` | `ok`; `attention` while a device is offline, naming them |
| `unifi:<instance id>/device/<mac, lowercase hex>` | `router` (types `udm`, `ugw`, `uxg`), `switch` (`usw`), `access_point` (`uap`), else `network_device` | `model` (UniFi's code, `UALR6v2`), `unifi_type`, `mac`, `ip`, `version`, `upgradable` (always), `upgrade_to` (when upgradable), `boot_at` (from the device's `last_seen − uptime`, to the minute, while it's up). A gateway has label `chadash.role: network-core` and cap `net.router` | From `state`: `1` online `ok`; `0` offline `down` (since `last_seen`); `4`, `5`, `2`/`7`, `8` updating, provisioning, adopting, deleting `maintenance`; `3`, `6`, `9`, `10`, `11` `attention`; others `unknown` |
| `unifi:<instance id>/wan` | `wan`, `WAN`. Only when the site has a gateway | `isp`, `gateway` (its name); cap `net.wan` | `wan` subsystem `error`: `down` "The WAN is down". Else `www` `error`: `down`, the internet doesn't answer. `warning` on either: `attention`. `ok`: `ok` |

Devices waiting for adoption (`adopted: false`) aren't the site's yet: they're only counted. The WAN's public address isn't published.

**Relations:** the instance `monitors` the site and each device; each device is `part_of` the site; a device's uplink `provides_network_for` it (from `uplink.uplink_mac`); the WAN is `part_of` the gateway (by `gw_mac`), else the site.

**Metrics:**

| Resource | Key | Unit | From |
|---|---|---|---|
| site | `clients.count`, `clients.wifi`, `clients.wired`, `clients.guests` | count | `stat/health`: `wlan` and `lan`'s `num_user` + `num_guest` |
| every device | `cpu.pct`, `mem.pct` | % | `system-stats`; unknown while offline |
| switch, access point | `clients.count` | count | `num_sta`; unknown while offline |
| WAN | `rx.mbps`, `tx.mbps` | Mb/s | `wan`'s `rx_bytes-r`, `tx_bytes-r` (bytes/s) × 8 |
| WAN | `latency.ms` | ms | `www`'s `latency`; unknown while down |

## Incidents

`wan.down` (scenario `wan-down`, problem, severity `down`), target the WAN, cause on the gateway, suggesting *Open UniFi Network*. Raised while the WAN is down as above, resolved when it's back. An offline device is health `down` on its resource, not an incident.

## Test connection

1. Signs in, then `GET …/api/self/sites`: the site must be there (otherwise it lists the ones the account sees). The account's role on it is the grant: `readonly` is *UniFi Network: View Only* (read); `admin` is admin, and any other role (Site Admin, Hotspot, custom) a write grant, so the framework warns on this read-only connection.
2. `stat/sysinfo` for the version.

## Repairs

| Situation | Result |
|---|---|
| Wrong username or password (UniFi OS `401`, standalone `400 api.err.Invalid`) | Repair, `auth`, field `password` |
| A UI.com account with two-factor authentication (`499 MFA_AUTH_REQUIRED`) | Repair, `auth`, field `username`: use a local account |
| The account has no role in UniFi Network (`403 api.err.NoPermission`) | Repair, `missing_permission`, field `username` |
| No such site (`400 api.err.NoSiteContext`) | Config error, `invalid_config`, field `site` |
| The login answers `404` (wrong platform, not UniFi) | Repair, `api_changed`, field `platform` |
| A read answers `404` or not the classic envelope | Repair, `api_changed`, field `url` |
| `429` (UniFi OS's sign-in limit), `5xx`, the Network app restarting | Transient; the coordinator backs off |

An expired session (`401`) signs in again once and retries; a `403` doesn't, since signing in again doesn't change a role.

## Limits

- **One site per instance.** Add an instance per site.
- **One WAN.** A gateway with two uplinks reports one `wan` subsystem; failover and per-uplink state (`uptime_stats` in newer versions) aren't read.
- **Models are UniFi's codes** (`UALR6v2`, not "U6 Long-Range"); the controller doesn't send the marketing name.
- **No clients listed**, only counts: client names and MACs would go to everyone who can see the tile.
- **Written from documentation.** The state codes and field names follow aiounifi and unpoller; until a console is recorded, a version that renamed something shows up as an `api_changed` repair or unknown values, not a crash.
