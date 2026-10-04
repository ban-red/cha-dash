# Paperless-ngx fixtures

**Synthetic.** Responses for `specs/paperlessngx.yaml`, written by hand from Paperless-ngx's REST API documentation and its source (`StatisticsView` and `SystemStatusView` in `src/documents/views.py` of [paperless-ngx/paperless-ngx](https://github.com/paperless-ngx/paperless-ngx)), in the shape Paperless-ngx 2.17 returns. No real instance was contacted; the numbers are made up.

| File | Endpoint | Notes |
|---|---|---|
| `statistics.json` | `GET /api/statistics/` (needs `Authorization: Token …`) | 2731 documents, 7 in the inbox, 43 tags, 118 correspondents, 21 document types, 4 storage paths |
| `status.json` | `GET /api/status/` (staff only) | Paperless-ngx 2.17.1 |
