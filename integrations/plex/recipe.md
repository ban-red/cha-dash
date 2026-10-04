---
title: Sign in with Plex
summary: Plex has no read-only tokens. A token acts as the Plex account, so it can manage the server and the account. Cha Dash needs the server owner's, because only the owner sees what's playing.
access: full
grants: [Plex account token]
discover:
  - {scheme: http, port: 32400, path: /identity, match: MediaContainer}
---

## Sign in with Plex

Press **Sign in with Plex** next to **Token**. A Plex page opens; sign in as the server's owner and allow Cha Dash. The token comes back into the form, and nothing is saved until you press Save.

## Notes

- Plex lists Cha Dash among your account's **Authorized Devices**, once however often you sign in. Removing it there revokes the token, and Cha Dash asks to reconnect.
- A token copied out of the Plex web app works too, and is the same kind of token.
- Cha Dash only reads what's playing and the library sizes.
