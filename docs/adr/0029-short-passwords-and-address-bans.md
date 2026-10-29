# ADR-0029 · Six-character passwords, backed by address bans

**Status:** accepted · **Date:** 2026-10-29

## Context

Account passwords needed at least 12 characters. In a home, accounts are often made for a partner, a kid or a wall tablet, and a 12-character rule pushes people to write the password down or give up on the account. The only defences against guessing were a per-client rate limit (10 attempts, then one every 6 s) and a per-account budget (20 wrong passwords, then one a minute). A shorter minimum needs something that stops a guesser for good, not just slows them down.

## Decision

**Account passwords need 6 characters. Five wrong passwords in a row from one client address ban that address from signing in until a person clears the ban.**

- The ban covers every sign-in method from that address, password and passkey. Sessions it already has keep working.
- Unknown usernames count as wrong. A sign-in that works resets the count.
- Bans are stored in the database (`ip_bans`) and never expire. Admins clear them in Settings → Users (`DELETE /api/bans/{ip}`, audited). When nobody can sign in, `chadash bans clear <ip>` on the hub's host clears one, with the hub running or not.
- The rate limit and the per-account budget stay as they were.
- Backup passphrases keep their 12-character minimum: an exported file can be attacked offline, where no ban applies.

## Consequences

- An attacker on one address gets five guesses, ever, until someone looks. Against a 6-character password that's far fewer than the old limits allowed over a day.
- **A person can lock themselves out.** The CLI is the way back in, so it has to keep working without a session.
- **Behind a reverse proxy without `CHADASH_TRUSTED_PROXIES`, every client is the proxy**, so five typos from anyone ban everyone. The deploy guide says to set it, and the CLI clears the ban.
- A ban covers one exact address. An IPv6 client can move to another address on its /64; the rate limit, which works per /64, still slows it down.
- The count of wrong passwords lives in memory, so a restart forgets it. Bans themselves survive restarts.
- Bans aren't in the audit log when they happen, only when they're cleared; the hub's log records each one at `warn`.

## Alternatives considered

- **Keep 12 characters.** Stronger on its own, but it's the rule that makes household accounts painful.
- **Bans that expire after a while.** Less lockout risk, but a patient guesser just waits; a ban someone has to clear also tells them it happened.
- **Lock the account instead of the address.** Anyone could lock the owner out by typing the owner's username five times.
- **Ban the IPv6 /64 instead of the address.** Harder to dodge, but the ban list and its route would carry prefixes, and in a home one /64 is often the whole LAN.
