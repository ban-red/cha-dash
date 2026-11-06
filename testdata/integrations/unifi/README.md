# UniFi fixtures: written, not recorded

The maintainer's lab has no UniFi, so **these were written by hand** from the classic UniFi Network API's documented shapes, as community clients (aiounifi, unpoller, the Art-of-WiFi PHP client) read them, for **UniFi Network Application 9.0.114 on UniFi OS 4.1.13**. They are not a capture. `bun run live:verify unifi` can't check them until someone records a real console; when that happens, replace these and keep the scheme below.

## Files

| File | Request | What it is |
|---|---|---|
| `login_unifios.json` | `POST /api/auth/login` (UniFi OS) | 200: the signed-in user. The session is the `TOKEN` cookie, the CSRF token the `X-CSRF-Token` header (the test server sets both) |
| `login_unifios_invalid_401.json` | same | 401 `AUTHENTICATION_FAILED_INVALID_CREDENTIALS` |
| `login_unifios_mfa_499.json` | same | 499 `MFA_AUTH_REQUIRED`: a UI.com account with two-factor authentication |
| `login_standalone.json` | `POST /api/login` (standalone) | 200 `{"meta":{"rc":"ok"}}`; the session is the `unifises` and `csrf_token` cookies |
| `login_standalone_invalid_400.json` | same | 400 `api.err.Invalid` |
| `login_required_401.json` | any read without a session | 401 `api.err.LoginRequired` |
| `no_site_400.json` | `GET …/api/s/<unknown>/…` | 400 `api.err.NoSiteContext` |
| `no_permission_403.json` | any read the account's role can't make | 403 `api.err.NoPermission` |
| `self_sites.json` | `GET …/api/self/sites` | Two sites, `default` and `x7k2p9qd`, with the account's `role` (`readonly`) |
| `sysinfo.json` | `GET …/api/s/default/stat/sysinfo` | `version` 9.0.114, `udm_version` 4.1.13 |
| `stat_device.json` | `GET …/api/s/default/stat/device` | A UDM Pro (`udm`), a 24-port switch (`usw`), an upgradable U6-LR (`uap`, `upgradable: true`, `upgrade_to_firmware`), an offline U6-Lite (`state: 0`) and an unadopted AP (`adopted: false`, `state: 2`). `system-stats` values are strings |
| `stat_health.json` | `GET …/api/s/default/stat/health` | Subsystems `wlan`, `wan`, `www`, `lan`, `vpn`, all `ok` but vpn (`unknown`). Rates (`rx_bytes-r`) are bytes per second |
| `stat_health_wan_down.json` | same | `wan` and `www` in `error` |
| `stat_health_no_gateway.json` | same | A site of APs behind another router: `num_gw: 0`, `wan` `unknown` |

`…` is `/proxy/network` on UniFi OS and nothing on a standalone controller.

## The scheme

| What | Fixture |
|---|---|
| LAN addresses | `10.0.40.x` |
| WAN, gateway and DNS addresses | `192.0.2.x` (TEST-NET-1) |
| MAC addresses | `00:00:5e:00:53:xx` (RFC 7042's documentation range) |
| UUIDs | `00000000-0000-4000-8000-…` |
| Object IDs, serials | `65000000…`, `0000000000nn` |
| Host names | `*.lab.example` |
| ISP | `Example ISP` |
| Secrets | `fake-…` |

`integrations/unifi/leak_test.go` checks every fixture against it.
