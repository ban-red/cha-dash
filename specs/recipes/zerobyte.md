---
title: An API key
summary: Zerobyte's API keys act as their user in one organization, with no read-only scope, so the key could start backups or change settings. Cha Dash only reads the backup schedules.
access: full
---

## Make the key

In Zerobyte, open **Settings → API keys** and create a key for the organization whose backups Cha Dash should show. Paste it into **API key**.

## Say what it backs up

A volume's folder sits on a host: an NFS or SFTP volume on its server, an agent's volume on that machine, and a directory volume on the machine Zerobyte runs on. Set **Host it backs up** to that machine's address (the URL's host is used otherwise) and, when Zerobyte runs in a container, **Path map** to its folder and the host's, such as `/data:/mnt/user`.

## Notes

- Written from Zerobyte v0.44.0's source (`app/server/modules/backups`, `app/server/modules/auth/auth.middleware.ts`, `packages/contracts/src/volumes.ts`), not checked against a running Zerobyte yet.
- Zerobyte keeps only each schedule's newest run, so after a failure the last good backup isn't known.
- SMB, WebDAV and rclone volumes name no host folder, so they cover nothing.
