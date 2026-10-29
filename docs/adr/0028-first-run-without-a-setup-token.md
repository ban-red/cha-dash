# ADR-0028 · First run without a setup token

**Status:** accepted · **Date:** 2026-10-29 · **Replaces:** the setup token in [ADR-0005](0005-v0-1-scope.md) ("First run: setup token → owner account → passkey") and [ADR-0007](0007-docker-compose-first.md) ("prints a setup token to the logs")

## Context

Until now a new hub logged a one-time setup token, and creating the owner account needed it. That kept anyone else on the network from claiming a fresh install before its owner did. It also made the first minute the clumsiest part of Cha Dash: install, open a terminal or the Unraid log viewer, find the line, copy the token, paste it. That cuts against the v0.1 promise of a live page within three minutes of `docker compose up -d`, and most installs sit on a home LAN where the person who installs it opens it right away.

## Decision

**A hub with no account opens straight to the first-run form. Whoever reaches it first creates the owner, with a password and a username that starts as `admin`.**

- `POST /api/setup` takes `{"username"?, "password"}`. An empty username means `admin`. It works once; after that it answers `409`.
- No token is minted or logged. Until the owner exists, the hub logs a first-run note at `warn` on every boot.
- Setup attempts stay rate-limited per client, like sign-in.

## Consequences

- **A new hub can be claimed by anyone who can reach it before its owner does.** That's an accepted risk, recorded in the [threat model](../plan/03-architecture.md#threat-model-summary-full-version-in-securitymd). The install docs say to create the owner straight after install, and the first-run note in the log names the state.
- A hub exposed to the internet before setup, through a port forward or a tunnel, is open to takeover until the owner exists. Create the owner before exposing a hub.
- One less step and no log reading on first run, on every install channel (Compose, Unraid, binaries).
- CI's smoke tests and the e2e suite look for the first-run note instead of a token.

## Alternatives considered

- **Keep the token.** Safest, but it's the step people get stuck on.
- **Keep the token and print a ready-made setup URL.** Still means reading the log, which is the step people get stuck on.
- **Allow setup only from the LAN or loopback.** Behind a reverse proxy every client looks local unless `CHADASH_TRUSTED_PROXIES` is set, and a hub on a VPS has no LAN, so the rule would protect little and confuse many.
