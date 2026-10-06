---
title: A control token
summary: A second user and privilege-separated token that can start, shut down, reboot, stop, snapshot and back up guests, and nothing else. Cha Dash keeps reading with the read-only token; this one is used only when you approve an action.
access: scoped
params:
  - name: user
    label: User
    default: chadash-control@pve
    pattern: '^[A-Za-z0-9][A-Za-z0-9._-]{0,62}@(pve|pam)$'
    help: A user of its own, so the read-only token stays read-only.
  - name: token
    label: Token name
    default: control
    pattern: '^[A-Za-z][A-Za-z0-9_-]{1,63}$'
  - name: role
    label: Role name
    default: ChaDashControl
    pattern: '^[A-Za-z][A-Za-z0-9_-]{1,63}$'
  - name: instance
    label: Name
    from: name
    default: pve1
    pattern: '^[a-z0-9][a-z0-9-]{0,62}$'
  - name: path
    label: Where it may act
    default: /
    pattern: '^/(pool/[A-Za-z0-9_-]{1,64})?$'
    help: "/ for every guest, or /pool/<name> for the guests and storage in one pool."
fills:
  control_token_id: '{{user}}!{{token}}'
grants: [Datastore.AllocateSpace, Datastore.Audit, Sys.Audit, VM.Audit, VM.Backup, VM.PowerMgmt, VM.Snapshot, VM.Snapshot.Rollback]
---

## Create the role

In a shell on any node of the cluster, as root. Besides what it reads to find a guest (`VM.Audit`, `Sys.Audit`, `Datastore.Audit`), the role holds what each action needs: start, shut down, reboot and stop (`VM.PowerMgmt`), snapshots (`VM.Snapshot`, and `VM.Snapshot.Rollback` to roll back), and backups (`VM.Backup` on the guest, `Datastore.AllocateSpace` on the storage).

```sh
pveum role add {{role}} --privs "VM.Audit Sys.Audit Datastore.Audit VM.PowerMgmt VM.Snapshot VM.Snapshot.Rollback VM.Backup Datastore.AllocateSpace"
```

## Create the user and its token

```sh
pveum user add {{user}} --comment "Cha Dash (control)"
pveum user token add {{user}} {{token}} --privsep 1 --comment "Cha Dash control: {{instance}}"
```

The second command prints the token's secret once. Paste it into **Control token secret**.

## Grant the role to both

A privilege-separated token gets the intersection of its own permissions and its user's, so both need the role.

```sh
pveum acl modify {{path}} --users {{user}} --roles {{role}}
pveum acl modify {{path}} --tokens '{{user}}!{{token}}' --roles {{role}}
```

## Notes

- To keep actions to a few guests, put them and their backup storage in a pool (`pveum pool add`) and grant on `/pool/<name>`. Actions on other guests then fail with Proxmox's refusal.
- The role can't change a guest's config, delete a guest, or touch hosts, users or storage settings. Rolling back and deleting snapshots can still lose data, so Cha Dash asks you to type the guest's name and confirm it's you first.
- Without this token, the guests' actions show as unavailable with a link here. The read-only token keeps working either way.
