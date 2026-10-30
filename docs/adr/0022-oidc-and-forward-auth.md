# ADR-0022 · OIDC links to local users; the owner keeps a local way in

**Status:** accepted · **Date:** 2026-10-22 · **Roadmap:** [decision 22](../plan/04-roadmap.md#decisions) · **Plan:** [09 · v0.3 → OIDC and forward-auth](../plan/09-v0.3.md#oidc-and-forward-auth)

## Context

Homelabs that run Authelia, Authentik, Pocket ID, Keycloak or Kanidm expect every app to sign in through it. Cha Dash has local users, passkeys, sessions, step-up and five roles, and `auth.StartSession` was written as the one place every sign-in method ends. OIDC was planned for v0.2 and slipped. The decisions are how an outside identity maps to a Cha Dash user and role, what step-up means for someone who signs in elsewhere, and how to avoid locking the owner out when the identity provider is down.

## Decision

**An OIDC identity is linked to a local user by issuer and subject, never by email, and the owner can always sign in locally.**

- **Authorization code with PKCE (S256), state and nonce**, through `coreos/go-oidc` and `golang.org/x/oauth2`. One provider in v0.3, configured by the owner in Settings → Security; the client secret goes in the vault.
- **Linking.** A signed-in user links their identity from their own settings. Provisioning a new user from a first sign-in is off by default; when on, it needs a group claim that maps to a role.
- **Roles from groups**, mapped by the owner. On every sign-in, a linked user's role becomes the highest role their groups map to, so the provider is where it changes. Losing every mapped group refuses the sign-in and ends that user's sessions. **The owner role never comes from a claim**, and the owner account is never provisioned.
- **Sessions are ours.** An OIDC sign-in ends in `StartSession` with method `oidc` and the usual sliding expiry. Cha Dash keeps no access or refresh tokens.
- **Step-up** uses a passkey when the user has one. Otherwise it is a fresh sign-in at the provider (`prompt=login`, `max_age=0`), accepted only if `auth_time` is under a minute old.
- **Forward-auth** is a separate opt-in. User and group headers (names configurable, Authelia's and Authentik's by default) count only when the TCP peer is in `CHADASH_TRUSTED_PROXIES`, and otherwise they are stripped. They map to linked users by username and create a normal session. Step-up never comes from a header.
- **Break-glass.** The password form can be hidden, but the owner's passkey and password sign-in can't be turned off.

## Consequences

- Two new tables (linked identities and short-lived login states) and a new sign-in event for the audit log and the socket rules.
- New public routes for starting and finishing a sign-in, owner-only settings routes, and link/unlink routes for the signed-in user.
- go-oidc and go-jose add to the hub binary. The size budget check in CI has to stay green. (See the implementation note: neither was needed.)
- Providers that don't send `auth_time` can't do step-up for users without a passkey. The settings page tests for it and says so.
- Multiple providers, back-channel logout and SCIM are left for later.
- **Implementation note (wave 13, 2026-10-30).** The client uses the standard library plus `golang-jwt/jwt/v5`, not go-oidc and x/oauth2. golang-jwt was already in the module graph through go-webauthn, so OIDC adds no module and the binary barely grows. The rest of this decision stands.

## Alternatives considered

- **Match users by email.** Simple, but an email claim a provider doesn't verify (or one that gets reused) would take over an account.
- **Make the provider the only way in.** Locks everyone out when it is down, and the IdP often runs on a host that Cha Dash is there to fix.
- **Keep the provider's tokens and refresh them.** More state and more secrets, for nothing Cha Dash needs after sign-in.
- **Let forward-auth headers satisfy step-up.** A proxy can't prove that someone authenticated recently, and a misconfigured proxy would hand out critical actions.
