# Tautulli fixtures (synthetic)

Synthetic responses for `specs/tautulli.yaml`, written by hand in the shape of Tautulli's API v2 as its [API reference](https://docs.tautulli.com/extending-tautulli/api-reference) and source (`plexpy/api2.py`) describe it. No real instance was contacted; the sessions, users and numbers are made up (the two titles are public-domain films).

Every command is `GET /api/v2?apikey=<key>&cmd=<command>` and answers inside `{"response": {"result", "message", "data"}}`.

| File | Command | Notes |
|---|---|---|
| `get_tautulli_info.json` | `get_tautulli_info` | `tautulli_version` `v2.15.3`, branch, install type |
| `get_activity.json` | `get_activity` | Two sessions, one transcoding. `stream_count` is a **string** (`"2"`), the per-decision counts are numbers, bandwidths are kbps |
| `get_history.json` | `get_history` | Three plays, newest first: `date` is Unix seconds, `watched_status` 1 once watched through, `percent_complete` how far it got |
| `server_status.json` | `server_status` | Tautulli's connection to Plex: `connected` |
| `unauthorized.json` | any, with a wrong `apikey` | Sent with HTTP 401 |
