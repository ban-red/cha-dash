# Proxmox VE action fixtures: synthetic

**These are not captures.** v0.1 never wrote to the lab, so there's nothing recorded to replay for writes. These files are written by hand from the Proxmox VE API documentation (`pve-docs` API viewer, PVE 8.4 and 9.x) for the action contract tests in `integrations/proxmox/actions_test.go`, in PVE's own `{"data": …}` envelope. The guests are the Demo Home's names on the sanitized `io` node.

| File | Shape of |
|---|---|
| `cluster-resources.json` | `GET /cluster/resources?type=vm`: guests with `node`, `status`, `lock`, `template`. The test's fake keeps the state from here and changes it as tasks finish |
| `backup-jobs.json` | `GET /cluster/backup`: an enabled job covering 201 and 203 on `pbs-main`, a disabled one on `local` |
| `vzdump-log.json` | `GET /nodes/{node}/tasks/{upid}/log`: a vzdump run to PBS, with the `INFO:  NN% (…)` progress lines the action turns into progress |

The fake (in the test) answers the writes as the documentation describes:

| Write | Answer |
|---|---|
| `POST /nodes/{node}/{qemu,lxc}/{vmid}/status/{start,shutdown,reboot,stop}` | `{"data": "UPID:…"}` |
| `POST /nodes/{node}/{type}/{vmid}/snapshot` (`snapname`, `description`, `vmstate`) | a UPID |
| `POST /nodes/{node}/{type}/{vmid}/snapshot/{snapname}/rollback` | a UPID |
| `DELETE /nodes/{node}/{type}/{vmid}/snapshot/{snapname}` | a UPID |
| `POST /nodes/{node}/vzdump` (`vmid`, `storage`, `mode`) | a UPID |
| `GET /nodes/{node}/tasks/{upid}/status` | `{"status": "running"}`, then `{"status": "stopped", "exitstatus": "OK"}` (or the error text) |
| A write the token may not do | `403 Permission check failed (/vms/201, VM.PowerMgmt)` in the status line, `{"data": null}` |

Not verified without a live run: the exact `exitstatus` text of a failed shutdown, and whether `VM.Snapshot.Rollback` alone (without `VM.Snapshot`) allows a rollback on 8.4.
