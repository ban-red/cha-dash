# Linkding fixtures (synthetic)

Synthetic responses for `specs/linkding.yaml`, written by hand in the shape of Linkding's API as its [API docs](https://linkding.link/api/) and source (`bookmarks/views/health.py`) describe it. No real instance was contacted; the bookmarks, tags and counts are made up.

The token goes in `Authorization: Token <token>`; a wrong one gets Django REST framework's 401.

| File | Endpoint | Auth | Notes |
|---|---|---|---|
| `health.json` | `GET /health` | public | `version` and `status` (`healthy`; a database failure answers 500) |
| `bookmarks.json` | `GET /api/bookmarks/?limit=1` | token | The paginated list: `count` is every unarchived bookmark of the token's user; one result |
| `bookmarks_archived.json` | `GET /api/bookmarks/archived/?limit=1` | token | The same for archived ones |
| `tags.json` | `GET /api/tags/?limit=1` | token | `count` is every tag |
