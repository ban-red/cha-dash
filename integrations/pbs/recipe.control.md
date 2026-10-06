---
title: A control token
summary: A second user and token that can prune one datastore by its retention policy. Cha Dash keeps reading with the Audit token; this one is used only when you approve a prune.
access: scoped
params:
  - name: user
    label: User
    default: chadash-control@pbs
    pattern: '^[A-Za-z0-9][A-Za-z0-9._-]{0,62}@(pbs|pam)$'
    help: A user of its own, so the Audit token stays read-only.
  - name: token
    label: Token name
    default: control
    pattern: '^[A-Za-z][A-Za-z0-9_-]{1,63}$'
  - name: store
    label: Datastore
    default: store1
    pattern: '^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$'
    help: The datastore Cha Dash may prune. Repeat the last step for each one.
fills:
  control_token_id: '{{user}}!{{token}}'
grants: [Datastore.Audit, Datastore.Modify, Datastore.Prune]
---

## Create the user

In a shell on the Proxmox Backup Server, as root:

```sh
proxmox-backup-manager user create {{user}} --comment "Cha Dash (control)"
```

## Create its token

```sh
proxmox-backup-manager user generate-token {{user}} {{token}}
```

It prints the token's secret once, as `value`. Paste it into **Control token secret**.

## Grant DatastoreAdmin on the datastore to both

Pruning every backup group in a datastore, not only the token's own, needs `Datastore.Modify`, which only the **DatastoreAdmin** role holds; PBS has no custom roles. Grant it on that one datastore, never on `/`.

```sh
proxmox-backup-manager acl update /datastore/{{store}} DatastoreAdmin --auth-id {{user}}
proxmox-backup-manager acl update /datastore/{{store}} DatastoreAdmin --auth-id '{{user}}!{{token}}'
```

## Notes

- **DatastoreAdmin can also read, restore and delete** the backups in that datastore. Cha Dash only ever prunes with it, by the policy of the datastore's own prune job, after a plan you confirm, and every prune is in the audit log.
- **DatastorePowerUser** isn't enough: it prunes only the backup groups the token owns, which is none.
- Without this token, the datastore's prune shows as unavailable with a link here. The Audit token keeps working either way.
