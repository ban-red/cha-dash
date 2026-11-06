---
title: An API token that can only read tunnels
summary: A Cloudflare API token with only the Cloudflare Tunnel Read permission can list tunnels and their connections, and can't change anything.
access: read-only
---

## Make the token

In Cloudflare's dashboard, open **My Profile → API Tokens** and choose **Create Token**, then **Create Custom Token**. Name it `chadash` and give it one permission: **Account → Cloudflare Tunnel → Read** (newer dashboards call it **Cloudflare One Connector: cloudflared → Read**). Under **Account Resources**, include only the account your tunnels are in. Create it and copy the token; it's shown once.

Paste it into **API token**. Set **URL** to `https://api.cloudflare.com/client/v4`, and **Account ID** to the 32-character ID on the account's overview page.

## Notes

- Cha Dash reads `GET /accounts/<account ID>/cfd_tunnel`, the first 50 tunnels that aren't deleted. Each tunnel's health is Cloudflare's own status: healthy is up, degraded needs attention, down (no connector connected) is down, and a tunnel that has never run is unknown.
- Connection counts and data centres come from the tunnel list, which Cloudflare has deprecated and may leave empty. Connectors' public IPs aren't read.
- Written from Cloudflare's API reference (fetched 2026-11-06), not from a live account.
