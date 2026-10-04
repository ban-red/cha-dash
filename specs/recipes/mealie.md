---
title: A token of a user who isn't an administrator
summary: Mealie's API tokens act as the user who made them; there are no read-only tokens. Make one as a user who isn't an administrator, in the household you want counted. It can still edit that household's recipes.
access: scoped
discover:
  - {scheme: http, port: 9925, path: /api/app/about, match: demoStatus}
---

## Use a user in the right household

Mealie counts recipes per household, so the token's user decides what the page shows. Use a user who isn't an administrator, or add one under **Settings → Users** as an administrator.

## Create the token as that user

Signed in as that user, open the profile, then **API Tokens**. Name it Cha Dash and create it. Mealie shows it once: paste it into **API token**.

## Notes

- An administrator's token works too, and it can also manage users and the server's settings.
