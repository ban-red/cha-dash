# Arcane fixtures

**Synthetic.** Responses for `specs/arcane.yaml`, written by hand from Arcane v2.17.2's source ([getarcaneapp/arcane](https://github.com/getarcaneapp/arcane), tag `v2.17.2`), in the shapes its Go types marshal to. No real instance was contacted; IDs, names, digests and paths are made up.

| File | Request | Source |
|---|---|---|
| `app-version.json` | `GET /api/app-version` (public) | `backend/internal/version/module.go` (route), `handler.go`, `service.go` (`GetAppVersionInfo`), `types/version/version.go` (`Info`) |
| `projects.json` | `GET /api/environments/0/projects?start=0&limit=50&sort=name&order=asc` with `X-API-Key` | `backend/internal/project/module.go` (route, `projects:list`), `handler.go` (`ListProjects`), `service.go` (`ListProjects`, `buildUpdateInfoSummary`), `children/listing/service.go` (`row`, `ProjectStatus`, `RuntimeServiceFromContainer`), `types/project/project.go` (`Details`, `RuntimeService`, `UpdateInfo`, `Tag`), `types/base/response.go` (`Paginated`), `types/image/image.go` (`UpdateInfo`) |
| `fixtures.yaml` | | What `chadash spec test specs/arcane.yaml specs/testdata/arcane` answers |

`projects.json` holds three projects: `media`, running, with an image update for sonarr; `paperless`, run by a GitOps sync, `partially running` with its broker exited and its webserver's health check `unhealthy`; `sandbox`, stopped, with no containers. A list row has no compose or `.env` content (`row` doesn't fill them), and Arcane fills `runtimeServices` only from containers that exist.

The auth header is `X-API-Key` (`backend/api/api.go`'s `ApiKeyAuth` scheme; `backend/internal/auth/handler.go`, `apiKeyHeaderAuth`). Keys are `arc_` and 64 hex digits (`backend/internal/apikey/service.go`). The local environment's ID is `"0"` (`backend/internal/environment/service.go`, `LocalEnvironmentID`).

Not checked against a live Arcane: the `$schema` value Huma adds to each body (its name follows `customSchemaNamer` in `backend/api/api.go`; the spec doesn't read it), and the body of a 401.
