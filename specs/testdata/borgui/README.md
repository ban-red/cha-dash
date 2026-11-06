# borg-ui fixtures

**Synthetic.** Responses for `specs/borgui.yaml`, written by hand from borg-ui v2.4.6's source ([karanhudia/borg-ui](https://github.com/karanhudia/borg-ui)): `_serialize_plan` and `_serialize_plan_run` in `app/api/backup_plans.py`, the source locations in `app/utils/source_locations.py`, the SSH connections in `app/api/ssh_keys.py`, the public `/api/system/info` in `app/api/system.py`, the token header in `app/core/security.py`, and `serialize_datetime` (ISO with `+00:00`). No real instance was contacted; names, hosts and paths are made up. Times are for 2026-11-06 06:00 UTC; the fixture test moves them to now.

| File | Request | Notes |
|---|---|---|
| `info.json` | `GET /api/system/info` (public) | Version 2.4.6 |
| `plans.json` | `GET /api/backup-plans/` | Four plans: local appdata nightly, a remote (SSH) source, a weekly local one, and one on an agent's machine with no schedule |
| `runs.json` | `GET /api/backup-plans/runs?limit=50` | Newest first: a good run, a failed remote run, one with warnings, a skipped run, and the weekly plan's last good run twelve days ago; `repositories` and `script_executions` left empty |
| `connections.json` | `GET /api/ssh-keys/connections` | The repository's storage host and the remote source's host |

Not checked against a live borg-ui: the full run payload (its repositories' backup jobs are left out here).
