# Checks (`checks`)

The built-in uptime checks: HTTP and TCP probes that need no credentials. It's the first Tier 0 integration and the reference for writing others.

**Tier:** silver (problems in the inbox, per-target TLS, fixture tests). **Capability:** `checks.uptime`. **Secrets:** none.

## Config

One instance holds a list of targets. The Homepage importer creates one instance named "Homepage", with a target per service Homepage monitors.

```json
{
  "tls": {"mode": "verify"},
  "targets": [
    {"name": "Sonarr", "kind": "http", "url": "https://sonarr.lab.example", "interval_s": 60},
    {"name": "Proxmox UI", "kind": "http", "url": "https://pve.lab.example:8006", "tls": {"mode": "tofu"}},
    {"name": "SSH on pluto", "kind": "tcp", "host": "10.0.11.10", "port": 22, "interval_s": 120},
    {"name": "Teapot", "kind": "http", "url": "http://teapot.lab.example", "expect_status": 418, "fail_after": 5}
  ]
}
```

| Key | Kind | Default | Meaning |
|---|---|---|---|
| `name` | both | required | Display name, unique within the instance. Its slug (lowercase letters and digits, other runs become `-`) ends the resource ID; when two names slug the same, the later one gets `-2`, `-3`, … |
| `kind` | both | required | `http` or `tcp` |
| `url` | http | required | The page to `GET`, `http://` or `https://` |
| `host`, `port` | tcp | required | Where to open a TCP connection |
| `expect_status` | http | any 2xx or 3xx | The status that means up |
| `interval_s` | both | `60` | Seconds between checks, at least 10 |
| `fail_after` | both | `3` | Consecutive failures before the target is down and a problem opens |
| `tls` | http | the instance's `tls` | `{"mode": "verify" \| "pin" \| "tofu" \| "insecure", "pin": "<sha256>"}` |

`fail_after` and the per-target `tls` are additions to the shared config keys in [07 · v0.1](../../docs/plan/07-v0.1.md#instance-config-keys); both are optional.

## What it publishes

Per target:
- **A resource** `check:<instance id>/<slug>`, kind `check`, named after the target (the web app finds a tile's check by kind and name), with `attrs.check` (`http`/`tcp`), `attrs.target` (the URL or `host:port`), `attrs.status_code` for HTTP, a link to the URL, and `caps: ["checks.uptime"]`. `integration:<instance id> monitors` it.
- **Health:** `ok` (state `online`) while it answers; `attention` after a failure, with the reason, until `fail_after` failures in a row; then `down` (state `error`), with `health.since` at the first failure. `unknown` until the first check.
- **A latency series**, `http.ms` (time to the response headers) or `tcp.ms` (time to connect), in ms on a 1–5000 scale, warn 800 / crit 2000 for HTTP and 200 / 1000 for TCP. A failed check's value is unknown (`null`), never 0.
- **A problem** in the inbox once the target is down: `<instance>.check.<slug>`, class `problem`, severity `down`, with the error, the status code and an "Open" action. It resolves itself when the target answers again.

The resource ID follows the name, so renaming a target makes a new resource. The correlator (wave 6) will tie these to the `app:` resources other integrations report.

## Behaviour
- **Always on.** Checks exist to raise problems, so they run at their interval whether or not anyone is watching (`Poll.Background`). The coordinator ticks at the fastest target's interval and each tick runs only the targets that are due. In the settings form, each target shows only the fields for its kind, and has its own **Test** button, which tests the instance with just that target and saves nothing. **Check now** on a check's problem (the `integration.refresh` action) runs every target at once, then the schedule carries on.
- **Up to 16 at once,** each with a timeout of 10 s or its interval, whichever is shorter.
- **Redirects aren't followed.** A 302 to a sign-in page (Authelia, Authentik) means the app is up.
- **TLS.** `verify` checks the chain and the host. `tofu` with no pin records the first certificate's SHA-256 **on the target** (`targets[i].tls.pin`), so targets with different certificates each keep their own pin; after that a different certificate makes the target fail with `tls_pin_mismatch`. `pin` and `insecure` behave as everywhere else. A certificate that fails verification doesn't make the target fail: the server answered, so the target is up with health `attention`, reported as untrusted with its fingerprint, and no problem is raised. Test connection lists it as a warning. That keeps a self-signed LAN service (Cockpit on 9090, say) from reading as down. A pin mismatch still fails the target.
- **Test connection** runs every target once and passes only if all of them answer; the message lists the ones that didn't.
- A failing target never fails the instance: the instance stays `ok` while its targets go up and down.

## Not yet
- ICMP ping: it needs raw sockets (or `CAP_NET_RAW`), which the hub doesn't have.
- Matching on response bodies, request headers or methods other than `GET`.
- Certificate expiry warnings.
