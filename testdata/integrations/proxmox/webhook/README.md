# PVE notification webhook fixtures

Bodies a PVE webhook target (PVE 8.3 and later) sends to `POST /api/hooks/pve/{instance}`, with the body template from `integrations/proxmox/README.md` ("Notifications"). Written by hand from PVE's documented template variables (`title`, `message`, `severity`, `timestamp`, `fields`), not captured: hosts are `*.lab.example`, addresses `10.0.30.x` (the scheme in ../README.md), IDs made up.

The hub never parses or shows a body (`internal/pvehook`): these files exist so its tests send something realistic, including markup that must never come back.

| File | What |
|---|---|
| `backup-failed.json` | A failed vzdump job on `wren`, PBS unreachable, with a `<script>` in the log lines |
| `fence.txt` | A fencing notice as plain text (PVE's default body is empty; some templates send text) |
