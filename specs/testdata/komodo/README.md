# Komodo fixtures

**Synthetic.** Responses for `specs/komodo.yaml`, written by hand from Komodo's API documentation (the `komodo_client` crate's docs: the `read` API's `GetVersion`, `ListStacks` and `ListServers`, and the `StackListItem`, `StackListItemInfo`, `StackState` and `ServerListItem` types) and its source ([moghtech/komodo](https://github.com/moghtech/komodo), `client/core/rs/src/api/read/` and `client/core/rs/src/entities/`), in the shape Komodo 1.19 returns. Every request is `POST /read` with `{"type", "params"}` and the `X-Api-Key` and `X-Api-Secret` headers. No real instance was contacted; IDs, names, addresses and hashes are made up, with `*.home.example` and `10.20.30.x` addresses.

| File | Request | Notes |
|---|---|---|
| `version.json` | `{"type": "GetVersion"}` | Komodo 1.19.5 |
| `stacks.json` | `{"type": "ListStacks"}` | Three stacks: `media` running on magpie with an image update for one service; `paperless` from git, unhealthy, with its branch past what's deployed; `sandbox` stopped |
| `servers.json` | `{"type": "ListServers"}` | magpie and egret |

Not checked against a live Komodo: `StackState`'s values beyond `running`, `stopped` and `unhealthy` (`deploying`, `paused`, `created`, `restarting`, `removing`, `dead`, `down`, `unknown`), and whether a stack's `status` is `null` or `""` when it isn't running (the spec handles both).
