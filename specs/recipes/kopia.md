---
title: The server-control password, as a basic auth header
summary: Kopia's server-control API lists the snapshot sources without the CSRF token its UI API wants. Its user can also start, pause and cancel snapshots and stop the server; Cha Dash only reads the sources.
access: full
---

## Start the server with a control password

Start Kopia's server with a server-control password of its own, beside the UI's:

```sh
kopia server start --address=0.0.0.0:51515 --server-control-username=server-control --server-control-password=a-long-random-password
```

## Encode it

Basic auth sends `user:password` in base64, and a spec can't encode it, so encode it once yourself:

```sh
printf '%s' 'server-control:a-long-random-password' | base64
```

Paste the output into **Basic auth**.

## Say what it backs up

Each source names its own client host (`root@nas:/data`), and that name is the host on the coverage map unless **Host it backs up** is set. When Kopia runs in a container, set **Path map** to its folder and the host's, such as `/data:/mnt/user`.

## Notes

- Written from Kopia v0.23.1's source (`internal/server/server.go`, `internal/serverapi`, `snapshot/manifest.go`), not checked against a running Kopia yet.
- Kopia reports only each source's newest snapshot. A snapshot with failed files or an incomplete one counts as failed; a snapshot that failed before Kopia saved it isn't listed, so it shows as stale once the schedule passes.
- With TLS on Kopia's server, give Cha Dash its certificate (TLS: verify with a custom CA).
