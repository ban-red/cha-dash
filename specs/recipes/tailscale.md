---
title: An API key, which can't be limited
summary: A Tailscale API key acts as the user who made it, and expires within 90 days. Cha Dash only lists the tailnet's devices with it.
access: full
---

## Make the key

In Tailscale's admin console, open **Settings → Keys** and choose **Generate API key**. Name it `chadash` and pick the longest expiry (90 days). Copy the key (it starts `tskey-api-`); it's shown once.

Paste it into **API key**, and set **URL** to `https://api.tailscale.com`.

## When the key expires

Tailscale API keys can't be made to last longer than 90 days. When this one expires, Cha Dash shows a sign-in Repair: generate a new key and paste it in. OAuth clients, which can be limited to reading devices and don't expire, need a token exchange a spec can't do yet.

## Notes

- Cha Dash reads `GET /api/v2/tailnet/-/devices`, the key's own tailnet. Each device becomes a host: online while it's connected to Tailscale, offline otherwise.
- A device whose key has expired is down; one whose key expires within 14 days (**Days before a key expires to warn**) needs attention. So does one waiting for approval, or one not seen for 7 days (**Days offline before attention**).
- Devices shared in from another tailnet are left out.
- Written from Tailscale's API reference (its OpenAPI document, fetched 2026-11-06), not from a live tailnet.
