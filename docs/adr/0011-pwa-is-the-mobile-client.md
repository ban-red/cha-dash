# ADR-0011 · The PWA is the mobile client

**Status:** accepted · **Date:** 2026-10-03 · **Roadmap:** [decision 11](../plan/04-roadmap.md#decisions)

## Context

"Couch on-call" is a core persona: a useful push, a clear diagnosis, and a fix in two taps from the phone. Native apps would mean two more codebases, store reviews and release trains for a small project. The web UI already derives a phone layout from the one canonical layout.

## Decision

**The installable PWA, served by the compose-hosted hub, is the mobile client. There's no native app for now.**
- Home Screen install, Web Push, and passkey approvals (WebAuthn, so Face ID, Touch ID or a fingerprint).
- A service worker caches the shell and the last snapshot, so the app shows last-known state when the hub can't be reached.

## Consequences

- One codebase for desk, phone, tablet and wall.
- **Install, push and passkeys need a secure context.** Plain `http://<host>:3696` works in a browser, but these features need HTTPS, so the install docs cover a reverse proxy with TLS.
- On iOS, Web Push only works once the app is added to the Home Screen. Onboarding has to say so.
- Web Push travels through the browser vendor's push service. It's opt-in, payloads are encrypted, and self-hosted channels (ntfy, webhooks) sit alongside it.
- Some things stay out of reach, such as home-screen widgets and reliable background refresh. We'll revisit if users need them.

## Alternatives considered

- **Native iOS and Android apps.** The best platform integration, but two more codebases and store gatekeeping.
- **A thin native wrapper around the web UI.** A store presence with little real gain, and still a review process.
- **Responsive web without PWA features.** No install, no push, no offline state.
