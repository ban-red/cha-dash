# Beszel fixtures

**Synthetic.** Responses for `specs/beszel.yaml`, written by hand from PocketBase's Web API documentation (records list: `page`, `perPage`, `totalItems`, `totalPages`, `items`; the `Authorization` header) and Beszel's source ([henrygd/beszel](https://github.com/henrygd/beszel): the `systems` collection's fields in the hub's migrations, `status` being `up`, `down`, `paused` or `pending`, and `info`'s short JSON keys in the agent's `system.Info` type: `h` hostname, `k` kernel, `c` cores, `t` threads, `m` CPU model, `u` uptime in seconds, `cpu`, `mp` and `dp` in percent, `b` bandwidth in MB/s, `v` the agent's version), in the shape Beszel 0.12 returns. No real instance was contacted; IDs, names, addresses and readings are made up, with `10.20.30.x` addresses, and the token is fake.

| File | Endpoint | Notes |
|---|---|---|
| `systems.json` | `GET /api/collections/systems/records?perPage=50&sort=name` | Three systems: egret up and fine, magpie up with its disk 93% full, stork down |
| `getkey.json` | `GET /api/beszel/getkey` | The hub's public key (fake) and version, 0.12.3 |

Not checked against a live Beszel: `getkey`'s `v` field (the spec reads it as optional, so a hub without it shows no version), and `info` keys added after 0.12 (the spec reads only the ones above).
