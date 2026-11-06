# Zerobyte fixtures

**Synthetic.** Responses for `specs/zerobyte.yaml`, written by hand from Zerobyte v0.44.0's source ([nicotsx/zerobyte](https://github.com/nicotsx/zerobyte)): the schedule schema in `app/server/modules/backups/backups.dto.ts`, the volumes in `packages/contracts/src/volumes.ts` (`publicVolumeSchema`), the run status written in `app/server/modules/backups/helpers/backup-lifecycle.ts`, and the `X-Api-Key` header in `app/server/modules/auth/auth.middleware.ts`. No real instance was contacted; names, hosts and paths are made up. Times are Unix milliseconds for 2026-11-06 06:00 UTC; the fixture test moves them to now.

| File | Request | Notes |
|---|---|---|
| `backups.json` | `GET /api/v1/backups` | Four schedules: a directory volume with include paths, a failed NFS volume, an agent's folder with a warning and nothing new for days, and an SMB volume turned off that never ran |

Not checked against a live Zerobyte: whether include paths are relative to the volume's root (the spec joins them to it) and the fields of a repository in the list.
