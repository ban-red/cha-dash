---
title: A forever token
summary: Duplicati signs in with a password for a short-lived token, a handshake a spec can't do, but it can issue one long-lived token. That token can do anything the web UI can; Cha Dash only reads the job list and the server's version.
access: full
---

## Let Duplicati issue it

Start Duplicati's server once with `--webservice-enable-forever-token=true`.

## Issue the token

On the machine running Duplicati:

```sh
duplicati-server-util issue-forever-token
```

It prints `Authorization: Bearer …`. Paste only the part after `Bearer ` into **Forever token**. Then restart Duplicati without the option, so nobody can issue another.

## Say what it backs up

Set **Host it backs up** to the address of the machine whose folders Duplicati reads (the URL's host is used otherwise). Duplicati's container usually mounts the host's `/` at `/source`: set **Path map** to `/source:/`.

## Notes

- Written from Duplicati v2.4.1.0's source (`WebserverCore/Endpoints/V1`, `Library/RestAPI/Runner.cs`), not checked against a running Duplicati yet.
- The token lasts ten years. To revoke it, restart Duplicati once with `--reset-jwt-config=true`.
- A job has failed when Duplicati's last error is newer than its last finished backup. That error can come from another operation on the job, such as a verify.
- Sources like `%MY_DOCUMENTS%` name no folder on a host, so they cover nothing.
