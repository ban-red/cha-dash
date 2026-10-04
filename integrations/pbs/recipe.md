---
title: A read-only API token
summary: A dedicated user with the Audit role and a token of its own. It sees datastores, jobs, tasks and the server's load, and can't read, restore or delete a backup.
access: read-only
params:
  - name: user
    label: User
    default: chadash@pbs
    pattern: '^[A-Za-z0-9][A-Za-z0-9._-]{0,62}@(pbs|pam)$'
    help: The @pbs realm keeps it out of the server's Linux users.
  - name: token
    label: Token name
    default: dash
    pattern: '^[A-Za-z][A-Za-z0-9_-]{1,63}$'
fills:
  token_id: '{{user}}!{{token}}'
grants: [Datastore.Audit, Sys.Audit]
discover:
  - {scheme: https, port: 8007, path: /, match: Proxmox Backup Server}
---

## Create the user

In a shell on the Proxmox Backup Server, as root:

```sh
proxmox-backup-manager user create {{user}} --comment "Cha Dash (read-only)"
```

## Create its token

```sh
proxmox-backup-manager user generate-token {{user}} {{token}}
```

It prints the token's secret once, as `value`. Paste it into **Token secret**.

## Grant Audit to both

A token can never do more than its user, so both get the **Audit** role on `/`: `Sys.Audit` and `Datastore.Audit`.

```sh
proxmox-backup-manager acl update / Audit --auth-id {{user}}
proxmox-backup-manager acl update / Audit --auth-id '{{user}}!{{token}}'
```

## Notes

- **DatastoreAudit** alone shows the datastores, but not other users' tasks or the server's CPU and memory.
- Don't grant **DatastoreReader** or **DatastoreBackup** for Cha Dash: they can read and restore every backup they cover.
