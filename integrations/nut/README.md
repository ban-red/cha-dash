# Network UPS Tools (`nut`)

Reads every UPS that a NUT server (upsd) serves: its status, battery charge, runtime, load and input voltage. It speaks NUT's own line protocol over plain TCP, port 3493. Read only. A low battery raises an incident at attention.

**Tier:** silver (repairs, version, fixture tests). **Capability:** `power.ups`. **Secret:** `password` (optional).

**Why Go, not a spec:** NUT isn't HTTP. The spec engine only does HTTP requests.

## Config

```json
{"host": "nas.home.arpa", "port": 3493, "username": "chadash", "password": "${secret:instance/<id>/password}"}
```

| Key | Meaning |
|---|---|
| `host` | The machine running upsd: a host name or an IP address, not a URL |
| `port` | upsd's port, default 3493 |
| `username` | Optional: a user from `upsd.users`. Plain config |
| `password` | That user's password: write-only, kept in the vault. Needs `username` |

Neither may hold a line break: they're sent on one protocol line, quoted and escaped.

**Credentials are usually not needed.** upsd lets any host that can connect run `LIST UPS` and `LIST VAR`. Stock upsd answers `OK` to `USERNAME` and `PASSWORD` and only checks them for commands that change something, which this integration never sends. So a wrong password goes unnoticed unless the server refuses it (see Repairs). When a password is set, Test warns (`plain_tcp`) that it crosses the network in clear.

## How it connects

Each sync opens one TCP connection with a `net.Dialer` inside `Sync` or `Test`, as the checks integration's TCP probes do: the framework has no TCP dialer, and `d.HTTP` doesn't apply. Connect does no I/O. The sync's deadline is the connection's, and cancelling the context closes it.

One session, in order:

| Command | When | Answer |
|---|---|---|
| `USERNAME "<user>"`, `PASSWORD "<password>"` | Only with a username (and password) | `OK` |
| `VER` | The first sync and every Test | `Network UPS Tools upsd 2.8.2 - …`; the word after `upsd` is the version. `ERR` (a very old upsd) leaves it empty |
| `LIST UPS` | Every sync | `BEGIN LIST UPS`, `UPS <name> "<description>"`…, `END LIST UPS` |
| `LIST VAR <ups>` | Every sync, for each UPS | `BEGIN LIST VAR <ups>`, `VAR <ups> <var> "<value>"`…, `END LIST VAR <ups>`, or `ERR DATA-STALE` and the like |
| `LOGOUT` | At the end, best effort | `OK Goodbye` |

Never `LOGIN`: that makes a client count as a upsmon secondary, and a primary waits for those before it shuts down. Never `SET`, `INSTCMD` or `FSD`.

Answers are parsed by NUT's rules: words split on whitespace, a double quote at the start of a word quotes it, a backslash escapes the next character. Limits: 4 KiB a line, 64 UPSes, 1000 variables a UPS. Anything past them, or anything that isn't the protocol, is a repair.

**Poll plan:** every 15 s, whether or not anyone watches (`Poll.Background`), with a 15 s timeout. A low battery leaves minutes, so the inbox should hear of it from an unwatched hub too. A sync is one short connection and a few kilobytes, lighter than upsmon's own 5 s polling.

## What it publishes

- **`nut:<instance id>/ups/<ups>`**, kind `ups`, one per UPS in `LIST UPS`. `<ups>` is upsd's name for it, with any character outside `A-Za-z0-9._-` as `-` and cut to 64 (a clash gets `-2`). Named after its description in `ups.conf`, or its upsd name when there's none (`Unavailable`). Caps `["power.ups"]`. `integration:<instance id> monitors` it.
- **Attrs:** `type: "ups"`, `provider: "nut"`, `ups` (the upsd name), `server` (`host:port`), `description`, `manufacturer` and `model` (`device.*`, else `ups.*`), `driver`, `battery_type`, `test_result`, `alarm`, `beeper`, `status` (raw `ups.status`), `flags` (its words), and the booleans `on_battery`, `low_battery`, `charging`, `replace_battery`. `error` is upsd's code when it can't read the UPS. Never the serial number.

**Metrics,** unknown (never 0) when the UPS doesn't report the variable:

| Key | Unit | Scale | Band | From |
|---|---|---|---|---|
| `battery.pct` | % | 0–100 | 90–100 | `battery.charge` |
| `runtime.min` | min | 0–240 | 10–240 | `battery.runtime` (seconds) ÷ 60, to 0.1 |
| `load.pct` | % | 0–100, warn 80, crit 95 | 0–60 | `ups.load` |
| `input.v` | V | 0 to 1.3 × nominal (else 260) | nominal ± 10 % (else 207–253) | `input.voltage`; `input.voltage.nominal` sets the scale. A power cut reads 0 V |
| `power.w` | W | 0 to `ups.realpower.nominal` (else 2000) | 0–60 % of it | `ups.realpower`, else `ups.load` × `ups.realpower.nominal` ÷ 100 |

**Runtime is in minutes** because that's how people read a UPS tile ("31 min left"); NUT reports seconds.

## State and health

From the words of `ups.status`, worst first:

| Status | State | Health |
|---|---|---|
| `OFF` | `off` | `down`: the output is off |
| `FSD` | as below | `down`: upsd is shutting systems down |
| `LB` | `on_battery` with `OB`, else `online` | `attention`, and the low-battery incident |
| `OB` | `on_battery` | `attention`: "on battery: the mains power is out, about 31 minutes left" |
| `RB`, `OVER`, `BYPASS`, `ALARM` | | `attention`, with the reason (and `ups.alarm`) |
| `CAL` | | `maintenance`: calibrating |
| `OL` (`CHRG`, `DISCHRG`, `TRIM`, `BOOST` only in `flags`) | `online` | `ok` |
| Anything else, or no `ups.status` | `unknown` | `unknown` |

Reasons add up ("on battery, and the battery is low; the battery needs replacing"). A UPS that upsd can't read (`ERR DATA-STALE`, `ERR DRIVER-NOT-CONNECTED`) is state `error`, health `attention`, with every metric unknown: nothing would warn of a power cut on it, so it isn't `unknown`.

**OB is attention, LB is the incident.** On battery means the UPS is doing its job: brownouts of a few seconds are common, and an incident for each would make the inbox flap. A low battery is when shutdown is minutes away, so it raises a problem (plan 15: "a low battery is an incident at attention"). Neither is `down`, because the load still has power: `down` is kept for `OFF` and `FSD`, when it doesn't or soon won't, like a stopped container elsewhere.

**The incident:** `ups.<ups>.low_battery`, scenario `ups-low-battery`, class `problem`, severity `attention`, on the UPS. Titled "Rack UPS is on battery, and the battery is low" (or "Rack UPS's battery is low" with `OL LB`, recharging after a cut), with the charge and runtime at the moment it opened. It's raised again unchanged on each sync while `LB` lasts, suggests Check now, and resolves when `LB` clears or the UPS leaves `LIST UPS`. While upsd can't read the UPS, an open incident stays open.

## Test connection

The same session with `VER`. Reports the upsd version; no grants (upsd can't say what a user may do). Warnings: `plain_tcp` with a password, `no_ups` when upsd serves none, `ups_unreadable` for each UPS upsd can't read.

## Repairs

| What happened | Error | Field |
|---|---|---|
| `ERR INVALID-PASSWORD`, `PASSWORD-REQUIRED` | `auth` Repair | `password` |
| `ERR INVALID-USERNAME`, `USERNAME-REQUIRED` | `auth` Repair | `username` |
| `ERR ACCESS-DENIED` on a command | `auth` Repair; it also names letting this host connect | `password`, or `username` without one |
| `ERR UNKNOWN-COMMAND`, an answer that isn't the protocol (an HTTP server on the port), a line over 4 KiB, a list over its limit | `api_changed` Repair: check the host and port | `port` |
| Refused, no route, DNS | `unreachable`, transient: the runtime backs off and marks what was published stale | |
| No answer before the sync's deadline | `timeout`, transient | |

Errors name the command and upsd's code, never the username's or password's value.

## Limits

- **No STARTTLS.** NUT can wrap the session in TLS, but few servers configure it; the password, when there is one, crosses in clear.
- **Not recorded.** No NUT server was at hand, so the fixtures are transcripts written from the [protocol docs](https://networkupstools.org/docs/developer-guide.chunked/net-protocol.html) and usbhid-ups variable names. A real upsd's answers should be recorded into them when one is available.
- **A wrong password goes unnoticed** on stock upsd, which checks it only for commands this integration never sends.
- No per-outlet data, no `ups.test.date`, no battery history beyond the series.

## Fixtures

Written, not recorded: [`testdata/integrations/nut/`](../../testdata/integrations/nut/README.md). The contract test (`nut_test.go`) serves them from a fake upsd on a loopback TCP listener and checks the client sends exactly the transcript's commands.
