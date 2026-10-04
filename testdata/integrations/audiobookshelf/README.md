# Audiobookshelf API fixture (synthetic)

**Synthetic.** Nothing here was captured from a real Audiobookshelf. The answers were written by hand from Audiobookshelf's API documentation ([api.audiobookshelf.org](https://api.audiobookshelf.org)) and its server source (`server/controllers/LibraryController.js`, `UserController.js`, `MeController.js` and `server/Server.js` in [advplyr/audiobookshelf](https://github.com/advplyr/audiobookshelf)), in the shape Audiobookshelf 2.26 returns, trimmed. Titles, names and ids are made up.

| File | Endpoint | Notes |
|---|---|---|
| `status.json` | `GET /status` (no key) | 2.26.3, set up |
| `me.json` | `GET /api/me` | A plain `user` with no download, upload, change or delete permission |
| `libraries.json` | `GET /api/libraries` | A book library and a podcast library |
| `stats_books.json` | `GET /api/libraries/<book library>/stats` | 124 items, 1 648 234.5 s (457.8 h) |
| `stats_podcasts.json` | `GET /api/libraries/<podcast library>/stats` | 9 podcasts, 2 161 440 s (600.4 h) |
| `users_online.json` | `GET /api/users/online` (admin only) | One open listening session |
