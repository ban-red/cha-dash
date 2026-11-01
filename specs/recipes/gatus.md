---
title: No credential, from a private address
summary: Gatus's API reads without a credential unless its security section is on, and it can't change anything. Cha Dash reads the endpoints' latest results from it.
access: none
discover:
  - {scheme: http, port: 8080, path: /api/v1/endpoints/statuses, match: conditionResults}
---

## Let Cha Dash reach it

Point **URL** at Gatus, such as `http://10.20.30.6:8080`. Cha Dash reads `GET /api/v1/endpoints/statuses`, the same list Gatus's own page shows, with each endpoint's newest result.

## If Gatus asks for a sign-in

With `security.basic` or `security.oidc` in Gatus's config, its API asks for a sign-in too, and this integration can't give one: basic auth and OIDC need code a spec can't hold. Either keep that security on a reverse proxy in front of Gatus's page and let Cha Dash reach Gatus directly on a private address, or leave Gatus out for now.

## Notes

- Each endpoint becomes a check in Cha Dash, named `group / name`, at most 50. Its latest result decides: passing is up, failing is down, with Gatus's error or the first failing condition as the reason.
- Response times are on each check as `response_ms`, not as a series: Gatus keeps the history itself.
