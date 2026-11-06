# Kopia fixtures

**Synthetic.** Responses for `specs/kopia.yaml`, written by hand from Kopia v0.23.1's source ([kopia/kopia](https://github.com/kopia/kopia)): the control API in `internal/server/server.go` (`/api/v1/control/sources`, basic auth as the server-control user, no CSRF token), `serverapi.SourcesResponse` and `SourceStatus` in `internal/serverapi/serverapi.go`, the snapshot manifest in `snapshot/manifest.go` and `snapshot/stats.go`, the directory summary in `fs/entry.go`, and the scheduling policy in `snapshot/policy/scheduling_policy.go`. No real instance was contacted; hosts, paths and sizes are made up. Times are for 2026-11-06 06:00 UTC, the clock the fixture test and `fixtures.yaml` (`now`) set.

| File | Request | Notes |
|---|---|---|
| `sources.json` | `GET /api/v1/control/sources` | Four sources: a daily one (times of day) with a good snapshot, a daily interval whose snapshot had two failed files, a six-hourly cron whose newest snapshot is days old, and another client's source run only by hand, never snapshotted |

Not checked against a live Kopia: whether a snapshot with failed files is saved with `numFailed` set (the spec treats it as failed) under every error-handling policy.
