---
title: One API key, read-only or with control
summary: An Unraid API key that reads the host, the array, Docker, VMs and the shares, plus its own grants. With Control chosen, it can also start, stop and update containers and start and stop VMs, and nothing else. It needs the Unraid API 4.37.4 or later.
access: read-only
params:
  - name: url
    label: Unraid address
    from: config.url
    default: https://unraid.lab.example
    pattern: '^https?://[A-Za-z0-9.-]+(:[0-9]{1,5})?$'
  - name: scopes
    label: Scopes
    from: config.mode
    default: 'info:read_any,array:read_any,docker:read_any,vms:read_any,share:read_any,me:read_any'
    options:
      - label: Read-only
        value: 'info:read_any,array:read_any,docker:read_any,vms:read_any,share:read_any,me:read_any'
        when: read_only
      - label: Control
        value: 'info:read_any,array:read_any,docker:read_any,docker:update_any,vms:read_any,vms:update_any,share:read_any,me:read_any'
        when: control
grants: [array, docker, info, me, share, vms]
discover:
  - {scheme: http, port: 80, path: /login, match: unraid}
---

## Choose read-only or control

Choose **Read-only, or let Cha Dash control Unraid?** in the form first. The link below asks for the scopes that choice needs. Read-only reads and changes nothing. Control adds two write scopes: Docker (start, stop and update containers) and VMs (start and shut down VMs). Every action still goes through Cha Dash's plan, confirmation and audit log.

## Open the consent page

Signed in to Unraid's web UI, open this address. It asks Unraid for a key named Cha Dash with exactly the scopes for your choice.

```url
{{url}}/Tools/ApiKeyAuthorize?name=Cha%20Dash&description=Cha%20Dash%20dashboard&scopes={{scopes}}
```

More than Cha Dash asks for by itself: copy one of these instead, if you want a key that can do more. They're fixed choices, so they ignore the form's read-only or control switch, and Test connection will say the key has more than that choice needs.

**Read and update everything.** Read and update access to Info, Array, Docker, VMs and Share, and read access to Me. Update on Array can also add and remove disks.

```url
{{url}}/Tools/ApiKeyAuthorize?name=Cha%20Dash&description=Cha%20Dash%20dashboard&scopes=info:read_any,info:update_any,array:read_any,array:update_any,docker:read_any,docker:update_any,vms:read_any,vms:update_any,share:read_any,share:update_any,me:read_any
```

**Read everything, full control of Docker and VMs.** Read access as in Read-only, plus update access to Docker and VMs and delete access to Docker. Update covers start, stop, restart, pause, update and reboot; delete is what removing a container needs. Unraid's API has no create or VM delete operation, so the key doesn't ask for them.

```url
{{url}}/Tools/ApiKeyAuthorize?name=Cha%20Dash&description=Cha%20Dash%20dashboard&scopes=info:read_any,array:read_any,docker:read_any,docker:update_any,docker:delete_any,vms:read_any,vms:update_any,share:read_any,me:read_any
```

## Create the key

Check what the page asks for: **read** access to Info, Array, Docker, VMs, Share and Me, and, for Control only, **update** access to Docker and VMs. Then create the key. Unraid shows it once: paste it into **API key**.

## Or create it by hand

Under **Settings → Management Access → API Keys**, create a key with read access (`read_any`) to exactly `info`, `array`, `docker`, `vms`, `share` and `me`. For Control, also give it update access (`update_any`) to `docker` and `vms`.

## Notes

- `info` and `array` are required. Without `docker`, `share` or `vms`, those parts stay empty, and the instance says so.
- `me` lets Test connection read the key's grants back and compare them with your choice. The consent page lets anyone edit the scopes before creating the key, so that's how you see you got what was asked for.
- Switching to Control later: change the choice, open the consent link again and paste the new key, or add Docker and VMs update access to the existing key under **Settings → Management Access → API Keys**. Nothing needs deleting.
- Cha Dash never asks for `array:update_any`: it would also let the key add and remove disks. Parity checks and stopping the array stay in Unraid.
- Not `role:viewer` or `role:admin`: viewer also reads logs, the flash drive and the settings, and admin can do anything.
- Before API 4.37.4, some "read-only" keys could change settings, so Cha Dash won't read from an older API.
