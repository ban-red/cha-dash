# Home Assistant fixtures: synthetic

**These fixtures are synthetic.** Nothing here was recorded from a real Home Assistant. They were written by hand on 2026-10-03 for the v0.1 Home Assistant integration's contract tests, following Home Assistant's public API documentation:
- the REST API (`developers.home-assistant.io/docs/api/rest`): `GET /api/`, `GET /api/config`, `GET /api/states` and `GET /api/history/period/<timestamp>` with `filter_entity_id`, `end_time`, `minimal_response` and `no_attributes`;
- the WebSocket API (`developers.home-assistant.io/docs/api/websocket`): the `auth_required` / `auth` / `auth_ok` handshake and the `auth/current_user` command.

Entity shapes (attributes, `state_class`, `device_class`, `last_reset` on a daily utility meter, `entity_id` on light and switch groups, `is_hue_group` on a Hue room) follow what those integrations put in their state attributes. Names are neutral (`light.kitchen`, `person.resident_a`, user "Dashboard"); the location is a city centre and the URL is `home.lab.example`.

## Files

| File | Request | What it is |
|---|---|---|
| `api.json` | `GET /api/` | `{"message": "API running."}` |
| `config.json` | `GET /api/config` | Version `2026.9.3`, `state: RUNNING`, time zone `Europe/Berlin` (UTC+2 on the test day, so local midnight is 22:00 UTC) |
| `states.json` | `GET /api/states` | 55 entities, below |
| `history_midnight.json` | `GET /api/history/period/2026-10-02T22:00:00Z?filter_entity_id=sensor.coffee_machine_energy,sensor.house_energy&end_time=…&minimal_response&no_attributes` | Each meter's state at local midnight: the first item of each list |
| `ws_current_user.json` | WebSocket `auth/current_user` | The result: user "Dashboard", `is_admin: true`, `is_owner: false` |

## What `states.json` holds, and what it should derive

| Domain | Entities | Derived |
|---|---|---|
| `person` | `resident_a` home, `resident_b` not_home, `resident_c` home | `people_home` 2 of 3 |
| `light` | kitchen, living room, desk on; hallway, bedroom off; porch unavailable; `light.downstairs` (a group: `attributes.entity_id`) and `light.lounge` (a Hue room: `is_hue_group`) on | `lights_on` 3 of 6: groups don't count twice |
| `switch` | coffee machine, server rack on; garden pump, fan off; `switch.all_plugs` (a group) on | `switches_on` 2 of 4 |
| `sensor`, `device_class: power` | house 1234 W, coffee machine 15.2 W, solar 0.8 kW, grid net −350 W, fridge unavailable | `power.kw` 1.234 from `sensor.house_power`, the largest non-negative reading |
| `sensor`, `device_class: energy` | `house_energy` 18234.56 kWh (18226.36 at midnight), `coffee_machine_energy` 153200 Wh (152900 at midnight), `house_energy_daily` 8.25 kWh (a utility meter whose `last_reset` is local midnight), `fridge_energy` unknown | `energy.kwh` 8.25 from the daily meter, the largest rise since midnight (house 8.20, coffee machine 0.30) |
| `binary_sensor`, `cover` | front door closed; back door, bedroom window, garage door (a cover) open; hallway motion on and living-room blinds open, which don't count | `open` 3 of 4 |
| `lock` | front door unlocked, back door locked | `locks_unlocked` 1 of 2 |
| battery | hallway motion 12 %, front door 64 % (sensors); smoke alarm low (a binary sensor) | `batteries_low` 2 of 3: "Hallway motion" (12), then "Smoke alarm" |
| `climate` | living room heating (21.46 °, set to 22, 46 % humidity), bedroom idle (18.9 °, set to 17), garage unavailable | `climate_items`: the first two; the unavailable one is skipped |
| `binary_sensor`, safety | kitchen smoke, utility room leak (`moisture`) and hallway CO, all off | `safety_tripped` 0 of 3 |
| `media_player` | living room speaker playing "Weightless" by Marconi Union, bedroom TV idle, kitchen display off | `media_playing` 1 of 3 |
| `update` | Home Assistant Core up to date; Music Assistant 2.5.0 → 2.6.1 and hallway sensor firmware 1.2.0 → 1.3.0 pending; garden pump firmware unavailable | `updates` 2 |
| `alarm_control_panel` | the house, `armed_home` | `alarm` |
| any | porch light, fridge power, garden pump firmware, garage thermostat unavailable | `unavailable` 4 |
| others | `sun.sun`, `zone.home`, a temperature, an automation, an input boolean | counted only in `entities` (55) |
