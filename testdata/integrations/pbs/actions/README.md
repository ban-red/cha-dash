# Proxmox Backup Server action fixtures: synthetic

**Not captures.** Written by hand from the PBS API documentation (`pbs-api-types` and the API viewer for PBS 3.x and 4.x) for `integrations/pbs/actions_test.go`, in PBS's `{"data": …}` envelope. Datastores and prune jobs come from the read fixture in `../jupiter/` (also synthetic).

| File | Shape of |
|---|---|
| `prune-log.json` | `GET /nodes/localhost/tasks/{upid}/log` for a prune run: the retention options, then `remove`/`keep` per snapshot, then `TASK OK` |

The test's fake answers `POST /admin/datastore/{store}/prune-datastore` (form fields `keep-last` … `keep-yearly`, `ns`, `max-depth`) with `{"data": "UPID:…"}`, then `GET /nodes/localhost/tasks/{upid}/status` with `{"status": "running"}` and `{"status": "stopped", "exitstatus": "OK"}`. A token without `Datastore.Modify` gets `403` with `permission check failed`.

Not verified without a live run: that `prune-datastore` with only `Datastore.Prune` prunes nothing but the token's own groups (the recipe asks for DatastoreAdmin because of it), and the exact text of the 403.
