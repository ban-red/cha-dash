---
title: Your REST API token, which has full access
summary: Each linkding user has one REST API token, and it can read, change and delete that user's bookmarks. There is no read-only token. The token decides whose bookmarks are counted.
access: full
discover:
  - {scheme: http, port: 9090, path: /, match: linkding}
---

## Copy the token

Signed in as the user whose bookmarks you want counted, open **Settings → Integrations → REST API** and copy the token into **API token**.

## Notes

- Cha Dash counts the token user's bookmarks, archived ones included, and their tags. Another user's token counts only that user's.
- Linkding can regenerate the token on the same page, which cuts off everything that uses it.
