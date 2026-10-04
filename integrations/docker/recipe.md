---
title: A read-only socket proxy
summary: Cha Dash reads Docker through a socket proxy that lets GET requests for containers, images, events and engine info through, and refuses everything else. No start or stop, no exec, no logs, no files.
access: read-only
params:
  - name: host
    label: Docker host
    default: docker.lab.example
    pattern: '^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$'
    help: The name or address Cha Dash reaches that host at.
  - name: port
    label: Port
    default: '2375'
    pattern: '^[1-9][0-9]{0,4}$'
  - name: hub
    label: Cha Dash's address
    default: 192.0.2.10
    pattern: '^[0-9A-Fa-f.:]+(/[0-9]{1,3})?$'
    help: The IP the Docker host sees Cha Dash connect from, or a subnet. The proxy refuses everyone else.
fills:
  endpoint: 'tcp://{{host}}:{{port}}'
grants: [containers, inspect, events, stats, info, images]
---

## On the host that runs Cha Dash

Nothing to create: the `compose.yaml` Cha Dash ships runs this proxy as `socket-proxy`, on a network only the hub can reach. Its endpoint is `tcp://socket-proxy:2375`.

## On another Docker host

Put this in a `compose.yaml` of its own on that host and run `docker compose up -d` there. It's the shipped allowlist, published on port {{port}} and limited to Cha Dash's address.

```yaml
services:
  socket-proxy:
    image: wollomatic/socket-proxy:1.13.1@sha256:3935b709275e4ec35d6ed5a5c4a1f0d01ed31eec5e7234efc3357ecd47689002
    restart: unless-stopped
    user: "0:0"
    read_only: true
    cap_drop: [ALL]
    security_opt:
      - no-new-privileges:true
    mem_limit: 64m
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
    ports:
      - "{{port}}:2375"
    command:
      - -listenip=0.0.0.0
      - -allowfrom={{hub}}
      - -allowHEAD=/(v1\.[0-9]+/)?_ping
      - -allowGET=/(v1\.[0-9]+/)?(_ping|version|info|events)
      - -allowGET=/(v1\.[0-9]+/)?containers/json
      - -allowGET=/(v1\.[0-9]+/)?containers/[a-zA-Z0-9][a-zA-Z0-9_.-]*/(json|stats)
      - -allowGET=/(v1\.[0-9]+/)?images/json
      - -allowGET=/(v1\.[0-9]+/)?images/[a-zA-Z0-9][a-zA-Z0-9_.:/@-]*/json
      - -watchdoginterval=3600
      - -stoponwatchdog
      - -shutdowngracetime=5
```

Then use the endpoint `tcp://{{host}}:{{port}}`.

## Notes

- Test connection checks what the endpoint lets through, with GET requests only, and says which of three kinds it is: a filtered proxy like this one, a broad proxy, or the raw Docker API.
- A proxy that passes every GET under `/containers/` (Tecnativa's `CONTAINERS=1`) also hands out every container's environment, logs and files. Test flags it.
- The Docker socket itself, or the raw API on port 2375, is root on the host for whoever reaches it. It works, and the Inbox keeps a hint about it.
