# Mealie fixtures (synthetic)

Synthetic responses for `specs/mealie.yaml`, written by hand in the shape of Mealie's API as its source defines it (`mealie/routes/app/app_about.py`, `mealie/routes/households/controller_household_self_service.py`, `mealie/schema/household/household_statistics.py`; models serialise in camelCase). No real instance was contacted; the counts are the Demo Home's (87 recipes, 8 categories, 240 tags), the rest is made up.

| File | Endpoint | Auth | Notes |
|---|---|---|---|
| `app_about.json` | `GET /api/app/about` | public | `version` (no leading `v` in current releases), sign-up and OIDC settings |
| `households_statistics.json` | `GET /api/households/statistics` | `Authorization: Bearer <token>` | The token's household: recipes, users, categories, tags, tools. Mealie 2.0 moved this from `/api/groups/statistics` |
