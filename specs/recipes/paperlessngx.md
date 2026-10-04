---
title: A view-only user's API token
summary: Paperless-ngx has per-model permissions, so a user of its own can be allowed to view and nothing else. Its token then reads the statistics and can't change a document. Cha Dash only reads the counts.
access: read-only
params:
  - name: user
    label: User name
    default: chadash
    pattern: '^[A-Za-z0-9][A-Za-z0-9._@+-]{0,31}$'
discover:
  - {scheme: http, port: 8000, path: /api/, match: '"documents"'}
---

## Add the user

In Paperless-ngx, **Settings → Users & Groups → Add user**: name it `{{user}}`, leave **Superuser** and **Staff** off, and under **Permissions** tick only **View** for Documents, Tags, Correspondents, Document Types, Storage Paths and Saved Views.

## Let it see the documents

Paperless-ngx counts only the documents a user may view. Documents nobody owns are visible to everyone; for owned ones, give `{{user}}` (or a group it's in) view permission, for example in the default permissions of your consumption templates or mail rules, or with **Permissions** in bulk edit.

## Make its token

Sign in as `{{user}}`, open **My Profile**, and press the button beside **API Auth Token** to create one. Paste it into **API token**.

## Notes

- The version comes from `/api/status/`, which only staff can read, so with this user Cha Dash shows no version. Everything else reads.
- Recreating the token in **My Profile** cuts off the old one.
