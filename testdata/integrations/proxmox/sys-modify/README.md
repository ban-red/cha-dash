# Proxmox VE with Sys.Modify: synthetic

**These are not captures.** The lab's tokens are read-only (`PVEAuditor`), so `pluto/nodes/pluto/apt/update.json` is PVE's 403 (`Sys.Modify`). These two files are written by hand, in PVE's own `{"data": …}` envelope, and laid over the `pluto` recording by `internal/updates/hostrows_chain_test.go`: the same instance, with a token that may read pending updates.

| File | Shape of |
|---|---|
| `access/permissions.json` | `GET /access/permissions`: pluto's recorded privileges plus `Sys.Modify` on `/nodes` |
| `nodes/pluto/apt/update.json` | `GET /nodes/pluto/apt/update`: three pending packages with PVE's fields (`Package`, `Title`, `Version`, `OldVersion`, `Origin`, `Priority`, `Section`, `Arch`, `Description`), from the `pve-docs` API viewer. One minor and two patch moves |
