---
title: A serverinfo token, which can only read the monitoring endpoint
summary: The Monitoring app (serverinfo) accepts a token of its own that reads its statistics and nothing else. It's the read-only way in; an administrator's app password works too, but it can do everything that administrator can.
access: read-only
params:
  - name: container
    label: Container name (Docker installs)
    default: nextcloud
    pattern: '^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$'
grants: [Nextcloud serverinfo token]
discover:
  - {scheme: http, port: 80, path: /status.php, match: '"productname"'}
  - {scheme: https, port: 443, path: /status.php, match: '"productname"'}
---

## Make a token and give it to Nextcloud

On the Nextcloud server, as the web server's user. The token never lands in your shell history; the last line prints it once.

```sh
TOKEN=$(openssl rand -hex 32)
sudo -u www-data php occ config:app:set serverinfo token --value "$TOKEN"
echo "$TOKEN"
```

In Docker, run `occ` inside the container instead:

```sh
TOKEN=$(openssl rand -hex 32)
docker exec -u www-data {{container}} php occ config:app:set serverinfo token --value "$TOKEN"
echo "$TOKEN"
```

## Fill in the form

Paste the token into **Serverinfo token**, and leave **Username** and **App password** empty.

## Notes

- Nextcloud has one serverinfo token. If another dashboard already uses one, reuse it rather than replacing it, or that dashboard stops reading.
- The Monitoring app ships with Nextcloud and is on by default. If Test connection says the endpoint is missing, enable it under **Apps**.
- Without a token, use an administrator's username and an app password (**Personal settings → Security → Devices & sessions**). serverinfo refuses anyone who isn't an administrator, and an app password can do everything its account can, so Cha Dash reports it as full access.
