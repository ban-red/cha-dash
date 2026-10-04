# Nextcloud serverinfo fixture (synthetic)

**Synthetic.** Nothing here was captured from a real Nextcloud. The answer was written by hand from the Monitoring app's source and README (`lib/Controller/ApiController.php`, `lib/SystemStatistics.php`, `lib/StorageStatistics.php`, `lib/ShareStatistics.php`, `lib/SessionStatistics.php` and `lib/PhpStatistics.php` in [nextcloud/serverinfo](https://github.com/nextcloud/serverinfo)), in the shape Nextcloud 30 returns, trimmed. The numbers are made up.

| File | Endpoint | Notes |
|---|---|---|
| `serverinfo.json` | `GET /ocs/v2.php/apps/serverinfo/api/v1/info?format=json&skipApps=true&skipUpdate=true` | Nextcloud 30.0.6.2, 6 users (1 active in the last 5 minutes, 4 in 24 hours), 248 113 files, 41 shares, 979 300 000 000 bytes free (912.0 GiB). No `apps` or `update`, as `skipApps` and `skipUpdate` ask |

The test's fake server wants `OCS-APIRequest: true` and either the `NC-Token` header or basic auth.
