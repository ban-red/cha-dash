---
title: A read-only administrator's API key
summary: A dedicated user with TrueNAS's Read-only Administrator privilege and an API key of its own. It sees pools, datasets, disks, apps, snapshot and replication tasks and alerts, and can't change, delete or read the files in them. TrueNAS 25.04 or later, over HTTPS.
access: read-only
params:
  - name: user
    label: User
    default: chadash
    pattern: '^[a-z_][a-z0-9_-]{0,31}$'
    help: A user of its own, so the key can be revoked without touching anyone else's.
grants: [READONLY_ADMIN]
---

## Create the user

In TrueNAS's web interface, open **Credentials → Users** and choose **Add**:

- **Username:** `{{user}}`, **Full Name:** `Cha Dash (read-only)`.
- Turn on **Disable Password**: the user signs in only with its API key.
- **Shell:** `nologin`. Leave **SMB User** and **SSH** off.

## Give it the Read-only Administrator privilege

Open **Credentials → Groups**, find **builtin_readonly_administrators** (the group behind the **Read-only Administrator** privilege), choose **Edit → Members**, add `{{user}}` and save.

Don't use **Full Admin** or **Sharing Admin**: both can change the system.

## Create its API key

Open the user menu at the top right, choose **API Keys → Add**, name it `cha-dash`, pick `{{user}}` as the **Username**, and save. TrueNAS shows the key once. Paste it into **API key**.

## Notes

- **Use the https:// address.** TrueNAS revokes an API key the moment it's used over plain HTTP, and Cha Dash refuses an http:// URL for that reason. A self-signed certificate is fine: choose **Trust on first use**.
- Keys can expire. An expired or revoked key shows as a repair on the instance; make a new one here.
- This integration's fixtures were written from TrueNAS 25.10's API schema, not recorded from a running system; `live:verify` can't check them until an instance is added.
