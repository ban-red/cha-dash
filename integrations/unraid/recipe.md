---
title: A read-only API key with five scopes
summary: An Unraid API key that reads the host, the array, Docker and the shares, plus its own grants, and nothing else. It needs the Unraid API 4.37.4 or later.
access: read-only
params:
  - name: url
    label: Unraid address
    from: config.url
    default: https://unraid.lab.example
    pattern: '^https?://[A-Za-z0-9.-]+(:[0-9]{1,5})?$'
grants: [array, docker, info, me, share]
discover:
  - {scheme: http, port: 80, path: /login, match: unraid}
---

## Open the consent page

Signed in to Unraid's web UI, open this address. It asks Unraid for a key named Cha Dash with exactly these read scopes.

```url
{{url}}/Tools/ApiKeyAuthorize?name=Cha%20Dash&description=Cha%20Dash%20reads%20only&scopes=info:read_any,array:read_any,docker:read_any,share:read_any,me:read_any
```

## Create the key

Check that the page asks only for **read** access to Info, Array, Docker, Share and Me, then create the key. Unraid shows it once: paste it into **API key**.

## Or create it by hand

Under **Settings → Management Access → API Keys**, create a key with read access (`read_any`) to exactly `info`, `array`, `docker`, `share` and `me`.

## Notes

- `info` and `array` are required. Without `docker` or `share` those parts stay empty, and the instance says so.
- `me` lets Test connection read the key's grants back. The consent page lets anyone edit the scopes before creating the key, so that's how you see you got what was asked for.
- Not `role:viewer`: it also reads logs, the flash drive and the settings.
- Before API 4.37.4, some "read-only" keys could change settings, so Cha Dash won't read from an older API.
