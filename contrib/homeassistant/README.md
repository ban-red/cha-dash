# Cha Dash for Home Assistant

A Home Assistant custom integration that reads Cha Dash: the home's health, its open problems and the updates waiting, as entities. It reads only; nothing here changes Cha Dash.

It lives here until it moves to a repository of its own ([plan 15](../../docs/plan/15-v0.5.md), W4), so HACS can install it from there.

## What you get

| Entity | What it says |
|---|---|
| `binary_sensor.cha_dash_problem` | On while anything needs a person; `headline` is the worst problem's title. The one to automate on |
| `sensor.cha_dash_health` | `ok`, `attention` or `down`; `top` lists the five worst open problems, with who's on them |
| `sensor.cha_dash_open_problems`, `…_down`, `…_acknowledged` | Counts |
| `sensor.cha_dash_updates_waiting` | Containers with an update available. Not there for a household account's token |

It polls `GET /api/summary` every 30 seconds. Problems are counted as the token's user sees them, so a household account's token counts only what its boards show.

An acknowledged problem that's down leaves health at `attention`, not `down`: someone is on it.

## Install

1. In Cha Dash, **Settings → API tokens → New token**, with the **Inbox read** scope (`inbox.read`) and nothing else.
2. Copy `custom_components/chadash` into Home Assistant's `config/custom_components/`, then restart Home Assistant.
3. **Settings → Devices & services → Add integration → Cha Dash**, then give its address (`https://dash.example.home`) and the token. It takes https, or plain http only to a local IP address, since the token rides every request. When the token is revoked, Home Assistant asks for a new one.

## An automation

```yaml
automation:
  - alias: Flash the hall light when the home needs someone
    triggers:
      - trigger: state
        entity_id: binary_sensor.cha_dash_problem
        to: "on"
    actions:
      - action: light.turn_on
        target: { entity_id: light.hall }
        data: { flash: short, color_name: orange }
```

## Tests

`python3 -m unittest discover -s tests` checks the summary parser with the standard library only. The Home Assistant parts are thin and follow Home Assistant's own coordinator pattern.
