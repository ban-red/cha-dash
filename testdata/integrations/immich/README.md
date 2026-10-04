# Immich fixtures (synthetic)

**Synthetic.** Written by hand for the Immich integration's contract tests ([`integrations/immich`](../../../integrations/immich/README.md)), in the shapes of Immich's API as its server source defines them (`server/src/controllers/*.controller.ts` and `server/src/dtos/*.dto.ts`, `main` as of 2026-10). No Immich instance was contacted. The numbers match the Demo Home's Immich (44,040 photos, 2,401 videos, 272.3 GiB, 2 users); the users, IDs and addresses are made up.

| File | Request | Auth | What it is |
|---|---|---|---|
| `server_version.json` | `GET /api/server/version` | public | `{major, minor, patch}`: 2.1.0 |
| `server_info_version.json` | `GET /api/server-info/version` | public | The pre-1.107 path, from an old server: 1.105.1 |
| `server_about.json` | `GET /api/server/about` | `server.about` | Version string, build components, `licensed` |
| `server_statistics.json` | `GET /api/server/statistics` | `server.statistics`, **admin only** | `photos`, `videos`, `usage` (bytes), and a row per user in `usageByUser` |
| `users_me.json` | `GET /api/users/me` | `user.read` | The key's user (a non-admin here), with `isAdmin` and `quotaUsageInBytes` |
| `assets_statistics.json` | `GET /api/assets/statistics` | `asset.statistics` | The key's own library: `images`, `videos`, `total` |
| `api_keys_me.json` | `GET /api/api-keys/me` | any key | The key in use and its `permissions` (a read-only set here) |
| `unauthorized_401.json` | any authenticated route | | Immich's answer to a key it doesn't know |
| `forbidden_403.json` | `GET /api/server/statistics` | | A valid key that lacks the permission. A non-admin's key gets a 403 too (message `Forbidden`) |

The API key header is `x-api-key`.
