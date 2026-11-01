# Dockhand fixtures

**Synthetic.** Responses for `specs/dockhand.yaml`, written by hand from Dockhand v1.0.56's source ([Finsys/dockhand](https://github.com/Finsys/dockhand), tag `v1.0.56`; image `fnsys/dockhand`), in the shapes its server code builds. No real instance was contacted; names, IDs and addresses are made up, with `10.107.x.x` container addresses.

| File | Request | Source |
|---|---|---|
| `stacks.json` | `GET /api/stacks?env=1`, with `Authorization: Bearer dh_…` when authentication is on | `src/routes/api/stacks/+server.ts` (`GET`: Docker's stacks, then the ones only in Dockhand's database with `status: "created"`), `src/lib/server/stacks.ts` (`listComposeStacks`, `ComposeStackInfo`, `ContainerDetail`), `src/lib/server/stack-status.ts` (`deriveStackStatus`, `deriveStackHealth`), `src/lib/server/docker.ts` (`listContainers`: full container IDs, health from the status line) |
| `unauthorized.json` | any `/api/` request without a valid token, with authentication on | `src/hooks.server.ts` |
| `fixtures.yaml` | | What `chadash spec test specs/dockhand.yaml specs/testdata/dockhand` answers |

`stacks.json` holds four stacks: `media`, running and healthy, with a digest update for sonarr; `paperless`, from git, running with its webserver `unhealthy`; `photos`, `partial`, its ml container exited 137 (no stack source row, so no `sourceType` or `icon`); `sandbox`, defined in Dockhand and never started, so it has only `name`, empty lists, `status`, `sourceType` and `icon`.

Tokens are `dh_` and 43 base64url characters (`src/lib/server/api-tokens.ts`); `hooks.server.ts` reads one only when the header starts `Bearer dh_`, and only when authentication is on. Without authentication every request is answered. Stack listing needs `stacks:view` (checked only in Enterprise; in the free edition every signed-in user may do everything, `src/lib/server/auth.ts`, `checkPermission`).

Not checked against a live Dockhand: the exact `labels` Docker Compose puts on containers (the spec doesn't read them), and the `newerVersion` object's shape (always `null` here).
