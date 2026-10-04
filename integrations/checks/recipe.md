---
title: No credential
summary: Checks only send an HTTP request or open a TCP connection, the way a browser or a port check would, and read whether it answered. Nothing to create.
access: none
---

## Add the targets

List what to check: an address to open over HTTP, or a host and port for TCP. Each target gets its own uptime and response time on the page.

## Notes

- A check that sends credentials in its URL sends them in clear over `http://`. Prefer a page that needs no sign-in, such as a health endpoint.
