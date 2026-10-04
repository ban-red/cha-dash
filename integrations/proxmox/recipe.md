---
title: A read-only API token
summary: A dedicated user, a role with only the audit privileges Cha Dash reads, and a privilege-separated token. Make one per Proxmox VE instance. Nothing it holds can start, stop or change a guest, a host or a setting.
access: read-only
params:
  - name: user
    label: User
    default: chadash@pve
    pattern: '^[A-Za-z0-9][A-Za-z0-9._-]{0,62}@(pve|pam)$'
    help: The @pve realm keeps it out of the host's Linux users.
  - name: token
    label: Token name
    default: dash
    pattern: '^[A-Za-z][A-Za-z0-9_-]{1,63}$'
  - name: role
    label: Role name
    default: ChaDashAudit
    pattern: '^[A-Za-z][A-Za-z0-9_-]{1,63}$'
  - name: instance
    label: Name
    from: name
    default: pve1
    pattern: '^[a-z0-9][a-z0-9-]{0,62}$'
  - name: privs
    label: Proxmox VE version
    help: 9.x adds VM.GuestAgent.Audit, which shows VM addresses from the guest agent.
    options:
      - label: 9.x
        value: Datastore.Audit Sys.Audit VM.Audit VM.GuestAgent.Audit
      - label: '8.4'
        value: Datastore.Audit Sys.Audit VM.Audit
fills:
  token_id: '{{user}}!{{token}}'
grants: [Datastore.Audit, Sys.Audit, VM.Audit, VM.GuestAgent.Audit]
discover:
  - {scheme: https, port: 8006, path: /, match: Proxmox Virtual Environment}
---

## Create the role

In a shell on any node of the cluster, as root. The role holds only what Cha Dash reads: hosts, tasks and backup jobs (`Sys.Audit`), guests (`VM.Audit`), storage (`Datastore.Audit`).

```sh
pveum role add {{role}} --privs "{{privs}}"
```

## Create the user and its token

```sh
pveum user add {{user}} --comment "Cha Dash (read-only)"
pveum user token add {{user}} {{token}} --privsep 1 --comment "Cha Dash: {{instance}}"
```

The second command prints the token's secret once. Paste it into **Token secret**.

## Grant the role to both

A privilege-separated token gets the intersection of its own permissions and its user's, so both need the role on `/`.

```sh
pveum acl modify / --users {{user}} --roles {{role}}
pveum acl modify / --tokens '{{user}}!{{token}}' --roles {{role}}
```

## Notes

- The built-in **PVEAuditor** role works too; it also holds `Pool.Audit`, `SDN.Audit` and `Mapping.Audit`, which Cha Dash doesn't read.
- A read-only token can't see pending updates (they need `Sys.Modify`) or list backup volumes. Backup results come from the backup tasks instead; add Proxmox Backup Server to see the backups themselves.
- Don't use a token of `root@pam`: without privilege separation it can do anything on the host.
