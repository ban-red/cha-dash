---
title: A user of its own, with an API token
summary: Dockhand's API tokens act as the user who made them. In Dockhand's free edition every signed-in user can do everything, so the token can change things even though Cha Dash only lists stacks; Enterprise roles can narrow it to viewing stacks. With Dockhand's authentication off, no token is needed or used.
access: full
params:
  - name: user
    label: User name
    default: chadash
    pattern: '^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$'
discover:
  - {scheme: http, port: 3000, path: /api/auth/settings, match: passkeysConfigurable}
  - {scheme: https, port: 3000, path: /api/auth/settings, match: passkeysConfigurable}
---

## If Dockhand's authentication is off

Dockhand answers its API without a sign-in until authentication is turned on (Settings → Authentication). Then leave **API token** empty: Cha Dash reads `GET /api/stacks` like Dockhand's own page does. Anyone who can reach Dockhand can do the same, so consider turning authentication on.

## Make a user for Cha Dash

With authentication on, add a user named `{{user}}` in **Settings → Authentication → Users**. In Dockhand Enterprise, give it a role (**Settings → Authentication → Roles**) with only view on stacks, for the environment Cha Dash reads. In the free edition there are no roles: every user can do everything, so keep this user's password somewhere safe and don't use it for anything else.

## Make its API token

Sign in as `{{user}}`, open its **profile** page and generate an API token (Dockhand asks for the password again). It starts `dh_` and is shown once: paste it into **API token**.

## Pick the environment

**Environment ID** is the number of the Docker environment to read, from **Settings → Environments**. The first one Dockhand made is usually `1`. One instance reads one environment; add another instance for each other host.

## Notes

- Each compose stack becomes a stack in Cha Dash, the first 50, named after its compose project.
- When Dockhand can't reach the environment's Docker, it answers with no stacks rather than an error, so an offline host shows as empty.
- Deleting the token in Dockhand cuts Cha Dash off at once.
