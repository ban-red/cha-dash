# Karakeep fixtures (synthetic)

Synthetic responses for `specs/karakeep.yaml`, written by hand in the shape of Karakeep's API as its source defines it (`packages/api/routes/version.ts`, `packages/api/routes/users.ts`, `packages/shared/types/users.ts`). No real instance was contacted; the version and bookmark count are the Demo Home's (0.30.2, 1,201 bookmarks), the rest is made up.

The key goes in `Authorization: Bearer <key>`.

| File | Endpoint | Auth | Notes |
|---|---|---|---|
| `version.json` | `GET /api/version` | public | `{version}`; `"unknown"` when the build doesn't stamp one. Older releases don't have it |
| `users_me_stats.json` | `GET /api/v1/users/me/stats` | key | The key's user: `numBookmarks`, `numFavorites`, `numArchived`, `numTags`, `numLists`, `numHighlights`, plus `bookmarksByType`, `topDomains` and `totalAssetSize`. The real response also has `assetsByType`, `bookmarkingActivity`, `tagUsage` and `bookmarksBySource`, left out here because the spec doesn't read them. Hoarder 0.22 didn't have this endpoint yet |
