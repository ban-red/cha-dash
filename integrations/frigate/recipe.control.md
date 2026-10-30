---
title: An admin account for control
summary: A second Frigate user with the admin role, used only when you approve an action that turns a camera, or its detection, recording, snapshots or review alerts, on or off. Cha Dash keeps reading with the viewer account.
access: full
params:
  - name: user
    label: User name
    default: chadash-control
    pattern: '^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$'
    help: A user of its own, so the viewer account stays read-only.
fills:
  control_username: '{{user}}'
grants: [Frigate admin]
---

## Create the user

In Frigate, **Settings → Users → Add User**. Name it `{{user}}`, give it a long random password, and pick the **admin** role. Frigate only takes camera switches from an admin, and it has no narrower role that can.

## Fill in the control fields

In this instance's settings, put `{{user}}` in **Control username** and its password in **Control password**. Cha Dash signs in with it only to send an approved switch.

## Notes

- An admin can also change Frigate's config, restart it and delete recordings. Cha Dash never does: an action sends the one switch it names and nothing else.
- Turning a whole camera on or off needs Frigate 0.16 or later.
- Frigate only turns recording, review alerts or a camera on when its config file enables them. Without that, Cha Dash doesn't offer the action.
- Without this account, the camera actions show as unavailable with a link here. The viewer account keeps working either way.
