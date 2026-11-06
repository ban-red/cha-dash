# Proxmox Backup Server action fixtures: synthetic

**Not captures.** Written by hand from the PBS API documentation (`pbs-api-types` and the API viewer for PBS 3.x and 4.x) for `integrations/pbs/actions_test.go`, in PBS's `{"data": …}` envelope. Datastores and prune jobs come from the read fixture in `../jupiter/` (also synthetic).

| File | Shape of |
|---|---|
| `prune-log.json` | `GET /nodes/localhost/tasks/{upid}/log` for a prune run: the retention options, then `remove`/`keep` per snapshot, then `TASK OK` |
| `verify-ok.json` | One Verify now exchange on `store-2`, the whole datastore: `start` is what `POST /admin/datastore/{store}/verify` answers (`{"data": "UPID:…:verify:store\x2d2:…"}`), `status` the task's `GET …/tasks/{upid}/status` answers in order (`running`, then `stopped` with `exitstatus: "OK"`), `log` its `GET …/log` (`total`, then `{n, t}` lines ending in `TASK OK`) |
| `verify-failed.json` | The same on `mars`, failing: a chunk that won't load, `Failed to verify the following snapshots/groups:`, `TASK ERROR: …`, and `exitstatus: "verification failed - please check the log for details"`, PBS's error for a verify with bad snapshots |

The test's fake answers `POST /admin/datastore/{store}/prune-datastore` (form fields `keep-last` … `keep-yearly`, `ns`, `max-depth`) with `{"data": "UPID:…"}`, then `GET /nodes/localhost/tasks/{upid}/status` with `{"status": "running"}` and `{"status": "stopped", "exitstatus": "OK"}`. A token without `Datastore.Modify` gets `403` with `permission check failed`.

The verify files are also hand-written, not recorded: `store` and the three parts are a test wrapper around PBS's own response bodies, which `verify_test.go` replays for that datastore. The status fields follow PBS's task status (`upid`, `node`, `pid`, `pstart`, `starttime`, `type`, `id`, `user`, `tokenid`, `status`, `exitstatus`); the log lines follow PBS's verify worker. Replace them with a capture once Verify now has run against a real PBS.

Not verified without a live run: that `prune-datastore` with only `Datastore.Prune` prunes nothing but the token's own groups (the recipe asks for DatastoreAdmin because of it), and the exact text of the 403.
