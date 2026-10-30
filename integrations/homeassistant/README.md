# Home Assistant (`homeassistant`)

Home Assistant's health and version, readings derived from its entity states (people home, lights on, switches on, house power and energy today, and what needs a look: open doors and windows, unlocked locks, low batteries, pending updates, unavailable entities, the alarm), and its calendars' events. REST only; Test also asks the WebSocket API whose token it holds.

**Tier:** silver (repairs, version detection, honest grants, fixture tests). **Capabilities:** `home.entities`, `calendar.events`. **Secrets:** `token`.

## Config

```json
{"url": "https://home.lab.example", "tls": {"mode": "verify"}, "token": "${secret:instance/<id>/token}"}
```

| Key | Required | Meaning |
|---|---|---|
| `url` | yes | Home Assistant's address, `http://` or `https://` (a path prefix is kept) |
| `tls` | no | The usual `{"mode", "pin"}` |
| `token` | yes | A long-lived access token (profile → Security). Sent as `Authorization: Bearer` on every request, and in the WebSocket's auth message; never in a URL |
| `power_entity` | no | `sensor.…`: the whole house's power sensor, when the default pick is wrong |
| `energy_entity` | no | `sensor.…`: the whole house's energy meter, when the default pick is wrong |
| `scripts` | no | `["script.movie_night", …]`, at most 50: the scripts Cha Dash may run (see Actions). Empty or missing: no script actions |
| `control` | no | Any of `["light", "switch", "media_player", "climate"]`: the kinds of device the tile's panel may control (see Actions). Empty or missing: no device actions |

`power_entity`, `energy_entity`, `scripts` and `control` are additions to the shared keys in [07 · v0.1](../../docs/plan/07-v0.1.md#instance-config-keys); both are optional, and the importer's shape (`url`, `tls`, `token`) validates as is.

## What it reads

| Endpoint | When | Why |
|---|---|---|
| `GET /api/config` | every sync | `version`, `state` (`RUNNING`, `STARTING`, …), `safe_mode`, `recovery_mode`, `time_zone` |
| `GET /api/states` | every watched sync; every 30 min while idle; after a failure | Every entity's state and attributes. It can be megabytes in a big home, hence the idle plan |
| `GET /api/history/period/<local midnight>?filter_entity_id=<meters>&end_time=<midnight + 1 min>&minimal_response&no_attributes` | once per local day, for up to 64 meters; a meter without a reading is asked again after an hour | Each energy meter's reading at local midnight, for energy today |
| `GET /api/calendars` | with `/api/states`, but at most every 5 min | The calendar entities, `[{entity_id, name}]`. A 404 (no calendar integration) means no calendars |
| `GET /api/calendars/<entity_id>?start=<local midnight>&end=<midnight + 14 days>` | after `/api/calendars`, for up to 20 calendars (by entity ID) | Each calendar's events in the window; Home Assistant expands recurring ones |
| `GET /api/` and WebSocket `/api/websocket` | Test only | The API answers, and `auth/current_user` |

**Poll plan.** `Poll.Interval` 60 s while watched, `Poll.Idle` 5 min otherwise, 30 s timeout. An idle sync reads only `/api/config`, which keeps health and repairs current; the readings and calendars refresh every 30 minutes (one point per history slot) until someone watches again.

The WebSocket isn't used for live state in v0.1: polling a minute apart is enough for counts. A `subscribe_events` channel while watched is the obvious next step.

## How the readings are derived

From `/api/states`, by entity domain and device class:

| Metric | Rule |
|---|---|
| `people_home` | `person.*` entities whose state is `home`. `attrs.people_total` counts all `person.*` |
| `lights_on` | `light.*` entities that are `on`, **groups excluded**: a light with an `entity_id` list in its attributes (light groups, old-style groups) or `is_hue_group` (a Hue room or zone). `attrs.lights_total` counts the same set, unavailable lights included |
| `switches_on` | `switch.*` entities that are `on`, groups (an `entity_id` list) excluded. `attrs.switches_total` likewise |
| `power.kw` | `sensor.*` with `device_class: power` and a numeric state in mW, W, kW, MW or GW, converted to kW. **The largest non-negative reading**, on the assumption that the whole-house meter reads at least as much as any one device; negative readings (a net meter exporting) are skipped. `power_entity` overrides the pick. `attrs.power_entity` names the sensor used |
| `energy.kwh` | Energy today. Candidates are `sensor.*` with `device_class: energy`, `state_class` `total_increasing` or `total`, and a numeric state in Wh, kWh, MWh, GWh or J, kJ, MJ, GJ, converted to kWh. A meter whose `last_reset` is at or after local midnight (a daily utility meter) counts its state; any other counts its rise since local midnight, from one history request a day (a drop means it was reset, and the state counts). **The largest rise wins**, on the same assumption. `energy_entity` overrides the pick. `attrs.energy_entity` names the meter used |

What needs a look, from the same states:

| Metric | Rule |
|---|---|
| `open` | `binary_sensor.*` with device class `door`, `window`, `garage_door` or `opening` that are `on`, and `cover.*` with device class `door`, `garage`, `gate` or `window` that are `open` or `opening`. Blinds, shades and motion sensors don't count. `attrs.openings_total` counts the same set |
| `locks_unlocked` | `lock.*` that are `unlocked` or `open`. `attrs.locks_total` counts all locks |
| `batteries_low` | `sensor.*` with device class `battery` in `%` below 20, and `binary_sensor.*` with device class `battery` that are `on` (low). `attrs.batteries_total` counts both kinds |
| `updates` | `update.*` that are `on`. An unavailable update entity doesn't count |
| `unavailable` | entities of any domain whose state is `unavailable` (`unknown` isn't) |

Who and what is where, for the tile's cards:

| Reading | Rule |
|---|---|
| `safety_tripped` | `binary_sensor.*` with device class `smoke`, `carbon_monoxide`, `gas` or `moisture` (a leak) that are `on`. `attrs.safety_total` counts all of them |
| `media_playing` | `media_player.*` that are `playing`. `attrs.media_total` counts all media players |
| climate | `climate.*` with a `current_temperature` (an unavailable one is skipped): `attrs.climate_items` |
| people | every `person.*`: `attrs.people_items`, with where they are |

"Local midnight" is midnight in Home Assistant's `time_zone`. The package embeds Go's `time/tzdata` (≈450 kB) because the hub's image is `scratch`, with no zoneinfo; an unknown zone falls back to UTC with a warning in the log.

Values nobody can read are unknown (`null`), never 0: no power sensor, no meter with a midnight reading (the recorder is off, or the meter is new today), or a chosen sensor that doesn't exist.

## What it publishes

**`homeassistant:<id>/app`**, kind `app`, named after the instance, desired `running`, `caps: ["home.entities"]`, a link to the configured URL. `integration:<id> monitors` it.

| Attr | From |
|---|---|
| `type` | `homeassistant` |
| `version` | `/api/config` |
| `url` | the configured URL |
| `entities` | how many entities `/api/states` listed |
| `people_total`, `lights_total`, `switches_total` | as above (the Demo Home's keys) |
| `power_entity`, `energy_entity` | the sensors the readings came from |
| `openings_total`, `locks_total`, `batteries_total` | as above |
| `open_items` | the open ones, `[{name, kind}]` (`kind` is the device class), at most 8 |
| `battery_items` | the low ones, lowest first, `[{name, level}]` (no `level` for a binary sensor), at most 8. "Battery", "battery level" and "battery low" are dropped from the end of the name, so it names the device |
| `update_items` | the pending updates, `[{name, installed, latest}]` (`name` is the update's `title`, else its friendly name), at most 8 |
| `people_items` | every person, `[{name, where, since}]`: `where` is `home`, `away` (for `not_home`), a zone's name, or `unknown`; `since` is when the state last changed (UTC) |
| `climate_items` | thermostats that report a temperature, `[{entity, name, current, target, humidity, action, mode, min, max, step}]` (`target`, `humidity`, `min`, `max` and `step` only when reported; `action` is `hvac_action`, else the mode; `mode` is the state), at most 8. Temperatures are in `temperature_unit` |
| `temperature_unit` | the unit Home Assistant is set to, from `/api/config`'s `unit_system` (`°C` or `°F`) |
| `safety_total`, `safety_items` | the safety sensors, and the tripped ones, `[{name, kind}]` (`kind` is the device class) |
| `media_total`, `media_items` | the media players, and the playing ones, `[{entity, name, title, artist}]` (`title` and `artist` only when reported) |
| `player_items` | every media player, playing first, then paused, then by name, `[{entity, name, state, title, artist}]`, at most 30 |
| `light_items`, `switch_items` | every light and every switch (groups excluded, as for the counts), on first, then by name, `[{entity, name, state, brightness}]` (`brightness` 0–100, a light's while it's on), at most 200 each. For the tile's panel |
| `control` | the instance's `control`, when set: what the panel may switch |
| `alarm` | the first `alarm_control_panel.*`'s state (`disarmed`, `armed_home`, `triggered`, …); missing without one |

| State and health | When |
|---|---|
| `running`, `ok` | `state: RUNNING` (or no state field: older versions) |
| `starting`, `attention` | `STARTING` or `NOT_RUNNING` |
| `running`, `attention` | `STOPPING`, `FINAL_WRITE`, `STOPPED`; `recovery_mode` (the configuration didn't load); `safe_mode` |

**Metrics** on the app, on the simulator's scales:

| Key | Unit | Scale | Band |
|---|---|---|---|
| `people_home` | count | 0 – people total | 0 – total |
| `lights_on` | count | 0 – lights total | 0 – total |
| `switches_on` | count | 0 – switches total | 0 – total |
| `power.kw` | kW | 0 – 12 | 0.6 – 2.2 |
| `energy.kwh` | kWh | 0 – 200 | 0 – 40 |
| `open` | count | 0 – openings total | 0 – total |
| `locks_unlocked` | count | 0 – locks total | 0 – total |
| `batteries_low` | count | 0 – batteries total | 0 – 0 |
| `updates` | count | 0 – 20 | 0 – 3 |
| `unavailable` | count | 0 – entities | 0 – 5 |
| `safety_tripped` | count | 0 – safety sensors | 0 – 0 |
| `media_playing` | count | 0 – media players | 0 – total |

**`homeassistant:<id>/calendar/<slug>`**, one per `calendar.*` entity (`<slug>` is the entity ID after `calendar.`), at most 20: kind `calendar`, named after the calendar, state `online`, `caps: ["calendar.events"]`, no metrics. `integration:<id> monitors` it and the app `exposes` it.

| Attr | From |
|---|---|
| `type` | `calendar` |
| `provider` | `homeassistant` |
| `time_zone` | Home Assistant's `time_zone` (`UTC` if it's unknown) |
| `events` | from local midnight today to 14 days ahead, soonest first, at most 60: `[{start, end, all_day, title, location?}]`. A timed event's `start` and `end` are RFC 3339 in Home Assistant's zone, with the offset; an all-day event's are dates (`YYYY-MM-DD`), `end` exclusive. An event still running at midnight is included. `title` is the summary; `location` only when set. **Descriptions are never published** |

A calendar whose events can't be read keeps its last events (logged at debug); a failing `/api/calendars` keeps the last calendars. The attr is `events`, not `calendar`, which the web reads as media releases.

Scopes: `graph` (the app, every sync), `states` (the readings, when `/api/states` was read), so the readings' `as_of` is when the states were read, and `calendars` (when the calendars were read).

## Test connection and grants

`GET /api/` (must say the API is running) and `GET /api/config` (the version), then the WebSocket API: wait for `auth_required`, send `{"type": "auth", "access_token": …}`, send `auth/current_user`, close. That's the only command it sends.

**A long-lived token carries all of its user's rights**, and Home Assistant has no read-only tokens, so the grant says so:
- **an administrator** (`is_admin`) → access `admin`: "can control every device, change the configuration and manage users". A non-admin user's token is safer.
- **a non-admin user** → access `rw`, write: such a user can still call services, so the token can still control devices.
- **the WebSocket doesn't answer** (a proxy that blocks it, say) → access `rw`, write, plus an info warning `ha_user_unknown`.

All of them are write grants, so the runtime adds its write-grant warning: Cha Dash only reads. The user's name appears in the grant's detail (Test results aren't stored).

## Repairs

- **401 or 403** (a revoked or wrong token) → an auth Repair on `token`.
- **404 or 410** on `/api/config` or `/api/states` (not Home Assistant, or the `api` integration isn't loaded), a `/api/config` without a version, or JSON it can't decode → an `api_changed` Repair.
- A failing history request is never a failed sync: energy today stays unknown, and it's logged at debug.

## Actions

| Action | Tier | Undo | Params | What it does | Verify |
|---|---|---|---|---|---|
| `homeassistant.script.run` | T1 | — (a script does what it does) | `{"script": "script.…"}`, one of the instance's `scripts` | `POST /api/services/script/turn_on` with `{"entity_id": …}`, which starts the script without waiting for it | `GET /api/states/<script>`: its `last_triggered` moved, or it's running |

- **Only allow-listed scripts.** The params schema is an enum of the instance's `scripts`, so anything else (another script, another service, an extra field) is `422 invalid` before anything is sent. No other service can be called. With no scripts listed, the instance has no actions.
- **Preconditions** (`409 precondition`): Home Assistant has no such script (deleted or renamed since it was listed), it's already running, or it's unavailable. The listing checks each allowed script and offers one "Run script.… in …" per script that can run now, with its params, so the sheet and ⌘K run it as is.
- **Credential:** the instance's token. Home Assistant's tokens carry their user's rights and have no scopes, so the allow-list is the limit; there's no separate control credential. A non-administrator user can still run scripts.
- **Audit:** the target and the script's entity ID; the token never appears in a plan, task log or audit record.

Device control, for the tile's panel. Each kind is offered only when the instance's `control` lists it:

| Action | Tier | Undo | Params | Calls | Verify |
|---|---|---|---|---|---|
| `homeassistant.entity.on`, `.off` | T1 | each other, same params | `{"entity": "light.…" \| "switch.…"}` | `<domain>.turn_on` / `turn_off` | the state is `on` / `off` |
| `homeassistant.media.play`, `.pause` | T1 | each other, same params | `{"entity": "media_player.…"}` | `media_player.media_play` / `media_pause` | `playing` / not `playing` |
| `homeassistant.climate.set` | T1 | — | `{"entity": "climate.…", "temperature": n}` | `climate.set_temperature` | its `temperature` attribute is the one asked |

- **Only allowed kinds.** The params' `entity` pattern names only the allowed domains (`^(light|switch)\.[a-z0-9_]+$` with both), so anything else, an extra field included, is `422 invalid` before anything is sent. Locks, covers and the alarm panel are never offered: opening a door or disarming from a tile is too easy to do by accident.
- **Preconditions** (`409 precondition`), each one read of `/api/states/<entity>`: Home Assistant has no such entity; it's `unavailable` or `unknown`; it's already as asked (on, off, playing); a player to pause isn't playing, or one to play is off; a thermostat is off, or the target is outside its `min_temp`–`max_temp`.
- **Not listed.** They target the app and name the entity in their params, so ⌘K and the listing (`GET /api/actions`) offer none of them: with no entity, the precondition refuses before any read. The tile's panel plans them with the entity it shows.
- **Household** may switch lights (when switches aren't allowed too: a switch can be a server's smart plug), play and pause, and set thermostats. Switches need Operator.
- **Credential:** the instance's token, as for scripts.

## Limits

- **The power and energy picks are heuristics.** A home with a split-tariff meter (two energy sensors), solar production larger than consumption, or no whole-house meter at all needs `power_entity`/`energy_entity`. Home Assistant's own Energy dashboard settings are only readable over the WebSocket (`energy/get_prefs`), which v0.1 doesn't use for polling.
- **Groups from other integrations** (zigbee2mqtt, deCONZ) that don't list `entity_id` or set `is_hue_group` count as single lights.
- **Names, not rooms.** The lists use friendly names; areas are in the registry (WebSocket only). Lock names aren't listed, only the count.
- **Hidden and disabled entities** count: `/api/states` doesn't say which are hidden (that's the entity registry, WebSocket only).
- **Calendars are read-only** and limited to 20, two weeks and 60 events each.
- **No resources for rooms, devices or entities** (calendars aside), no incidents (Home Assistant's repairs and updates aren't read), and no live updates. The panel's lists are attrs on the app, read once a minute, so a change made from the panel shows as asked for until the next read.
- **On and off only.** Lights get no brightness or colour control, and media players no volume or source.
