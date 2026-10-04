# SABnzbd fixtures (synthetic)

Synthetic responses for `specs/sabnzbd.yaml`, written by hand in the shape of SABnzbd 4.5's API as its [API reference](https://sabnzbd.org/wiki/configuration/4.5/api) and source (`sabnzbd/api.py`, `sabnzbd/interface.py`) describe it. No real instance was contacted; the job and the numbers are made up (the one job is named after a Blender Foundation open movie).

Every call is `GET /api?mode=<mode>&output=json`, with `apikey=<key>` except for `mode=version`.

| File | Mode | Notes |
|---|---|---|
| `version.json` | `version` | `4.5.3`; needs no key |
| `queue.json` | `queue&limit=1` | Downloading at `kbpersec` `"2295.41"` (KiB/s, a **string**: 18.8 Mb/s, the Demo Home's qBittorrent speed), 3 jobs (`noofslots_total`), `mbleft` `"6655.28"` (MiB), a speed limit of `"100"` %, 1288.42 GiB free; `slots` holds only the first job because of `limit=1` |
| `queue_paused.json` | `queue&limit=1` | The same queue paused: `paused: true`, status `Paused`, speed `"0.00"`, no speed limit (`""`) |
| `error_apikey.json` | any keyed mode | What releases before the 403 answer to a wrong key, inside a 200 |

Recent SABnzbd answers a wrong or missing key with HTTP 403; the test's fake server answers 401, which the engine treats the same.
