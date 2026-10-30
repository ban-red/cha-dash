---
title: A calendar's private iCal address
summary: Most calendar apps publish a calendar as an .ics address that can only be read. Anyone with the address can read the calendar, so Cha Dash keeps it like a password and never shows it.
access: read-only
grants: [calendar]
---

## Google Calendar

1. On a computer, open Google Calendar and go to **Settings**. Under **Settings for my calendars**, pick the calendar.
2. Under **Integrate calendar**, copy **Secret address in iCal format**. It ends in `basic.ics`.
3. Paste it into **Feed address**. Add one connection per calendar.

Use the secret address, not the public one: the public address only works if the calendar is public. If the secret address ever leaks, **Reset** it there and paste the new one.

## iCloud

1. In the Calendar app on a Mac, or at iCloud.com, open the calendar's sharing options and turn on **Public Calendar**.
2. Copy the address it shows (`webcal://…`) and paste it into **Feed address**. Cha Dash reads `webcal://` as `https://`.

A public iCloud calendar can be read by anyone with its address.

## Outlook and Microsoft 365

1. In Outlook on the web, open **Settings → Calendar → Shared calendars**.
2. Under **Publish a calendar**, pick the calendar and **Can view all details** (or **Can view titles and locations**), then **Publish**.
3. Copy the **ICS** link and paste it into **Feed address**.

## Notes

- Nextcloud, Fastmail, Proton and most other calendars have an export or subscription link ending in `.ics`; any of them works.
- Cha Dash reads the feed every 15 minutes while someone watches and hourly otherwise. Google refreshes its feeds only every few hours, so changes can take that long to show.
- Only titles, times and places are kept. Descriptions, guests and links never leave the hub's memory.
