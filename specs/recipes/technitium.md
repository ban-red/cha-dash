---
title: A token for a user that can only view the dashboard
summary: Technitium's API tokens act as their user. A user with only Dashboard View can read the stats and can't change anything.
access: read-only
discover:
  - {scheme: http, port: 5380, path: /, match: Technitium DNS Server}
---

## Make a user

In Technitium's web console, open **Administration → Users** and add a user named `chadash` with a long random password. Leave it out of every group. Then open **Administration → Permissions**, choose **Dashboard**, add the `chadash` user and tick only **View**.

## Make the token

Still in **Administration**, open **Sessions** and choose **Create Token**. Pick the `chadash` user, name the token `chadash`, and copy it; it's shown once and doesn't expire.

Paste it into **API token**, and point **URL** at Technitium's web console, such as `http://10.20.30.53:5380`.

## Notes

- Cha Dash needs Technitium 15.0 or later: it sends the token in the `Authorization` header, never in the URL. Older servers say the token is rejected.
- Counts are for the last 24 hours, like Pi-hole's and AdGuard Home's. Health uses the last hour: when 5% or more of its queries fail to resolve (SERVFAIL), it needs attention (**Failed queries before attention**).
- Whether blocking is on isn't read: that's in the settings, which a view-only dashboard user can't see.
- Written from Technitium's API documentation (APIDOCS.md for 15.6.2, fetched 2026-11-06), not from a live server.
