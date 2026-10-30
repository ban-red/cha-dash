# Calendar feed (`ical`)

Reads one iCalendar (`.ics`) feed and publishes its events from today to two weeks ahead: Google Calendar's "Secret address in iCal format", a public iCloud calendar, a published Outlook calendar, Nextcloud, or any other `.ics` address. It feeds the Calendar widget. There's no OAuth: the address is the credential.

**Tier:** bronze, Tier 0 (Go). **Capabilities:** `calendar.events`. **Secrets:** `feed_url`.

Connect does no I/O. Each sync is one `GET` of the feed.

## Config

```json
{"name": "Family"}
```

with the secret `feed_url`.

| Key | Required | Meaning |
|---|---|---|
| `feed_url` | yes | The feed's address, **a secret** (write-only, kept in the vault): Google's carries a private token in its path, and anyone with it can read the calendar. `https://`, `http://` or `webcal://` (and `webcals://`), which is read as `https://` |
| `name` | no | The calendar's name on the widget, up to 64 characters. Defaults to the feed's `X-WR-CALNAME`, else the instance's name |
| `tls` | no | The usual `{"mode", "pin"}`, for a self-hosted server |

**The address never leaks.** The runtime's redactor only matches a secret whole, and the framework's own errors keep a URL's path (`CheckResponse`, `url.Error`). So this integration words every error itself, with the host at most: "the feed answered 404 Not Found", "couldn't reach calendar.google.com: …", "that address isn't an iCal feed". Nothing it logs, publishes or answers to Test carries the address, and a test checks that for every failure.

## What it reads

The whole feed (at most the runtime's 16 MiB response cap), parsed with [emersion/go-ical](https://github.com/emersion/go-ical) (MIT). Recurrences are expanded with [teambition/rrule-go](https://github.com/teambition/rrule-go) (MIT).

- **`VEVENT`s:** `DTSTART`, `DTEND` or `DURATION`, `SUMMARY`, `LOCATION`, `STATUS`, `UID`, `RRULE`, `RDATE` (lists and periods), `EXDATE` (lists, and dates on timed events), and `RECURRENCE-ID` overrides, which replace their instance. `STATUS:CANCELLED` drops an event, an instance, or a whole series with its overrides. An override whose series isn't in the feed (an invitation to one instance) is an event of its own.
- **`X-WR-CALNAME`** names the calendar; **`X-WR-TIMEZONE`** is its zone.
- **Time zones.** A `TZID` is read as an IANA name (also vendor-prefixed, `/mozilla.org/…/Europe/Berlin`), else as a Windows name through a small table (Outlook writes `W. Europe Standard Time`), else from the feed's own `VTIMEZONE` (its `STANDARD` and `DAYLIGHT` rules). The hub's image has no zone database, so in production the `VTIMEZONE`s do most of the work; Google, iCloud and Outlook include one for every zone they use, and the tests check that both paths agree. A zone none of these knows is read as UTC, with an `unknown_time_zone` warning on the instance.
- **The calendar's zone** is `X-WR-TIMEZONE`, else the first `VTIMEZONE`, else the first event's `TZID`, else UTC. It sets "today", all-day and floating times, and the offset events are shown in.
- **Never read:** descriptions, attendees, organisers, URLs, alarms, attachments. They stay in the response body and are dropped with it.

## What it publishes

One resource per feed, **`ical:<instance id>/calendar/main`**, kind **`calendar`**, named as above, state `online`, health `ok`, caps `["calendar.events"]`. `integration:<instance id>` `monitors` it. No metrics. This is the shared calendar shape in [`internal/README.md`](../../internal/README.md#integrations), which Home Assistant's calendars publish too.

| Attr | Type | Meaning |
|---|---|---|
| `type` | string | always `"calendar"` |
| `provider` | string | always `"ical"` |
| `time_zone` | string | the calendar's IANA zone, e.g. `"Europe/London"`; left out when the feed doesn't name one (a custom `VTIMEZONE`, or none) |
| `events` | array | below; `[]` when there are none |

**`events`**: soonest first, from local midnight today (in the calendar's zone) to 14 days ahead, at most 60, the soonest kept. An event that began before today and is still running (a conference since Thursday) is included. All-day events sort before timed ones that start at the same moment.

| Key | Type | Meaning |
|---|---|---|
| `start` | string | timed: RFC 3339 with the calendar's offset at that moment (`"2026-10-06T11:00:00+01:00"`; `Z` when the offset is 0); all-day: `"YYYY-MM-DD"` |
| `end` | string | the same form; for all-day events **exclusive** (a one-day event on the 9th ends `"2026-10-10"`) |
| `all_day` | boolean | a date, not a time |
| `title` | string | `SUMMARY` on one line, at most 200 characters; `"Untitled event"` when there's none |
| `location` | string | `LOCATION` on one line, at most 200 characters; left out when empty |

### Example

From the synthetic Google-style fixture (2026-10-03 15:05 in London), shortened:

```json
{
  "id": "ical:i_x/calendar/main", "kind": "calendar", "name": "Household", "source": "integration:i_x",
  "state": "online", "health": "ok",
  "attrs": {
    "type": "calendar", "provider": "ical", "time_zone": "Europe/London",
    "events": [
      {"start": "2026-10-01", "end": "2026-10-04", "all_day": true, "title": "Conference", "location": "Exhibition centre"},
      {"start": "2026-10-03T19:00:00+01:00", "end": "2026-10-03T22:00:00+01:00", "all_day": false, "title": "Film night", "location": "Community hall, Main Street"},
      {"start": "2026-10-06T11:00:00+01:00", "end": "2026-10-06T11:30:00+01:00", "all_day": false, "title": "Weekly sync (moved)"}
    ],
    "caps": ["calendar.events"]
  }
}
```

## Polling

Every **15 minutes** while someone watches and **hourly** otherwise, with a 30-second timeout. Google regenerates its feeds every few hours, so reading faster only costs requests.

## Grants

Test makes one fetch and reports one read grant, `calendar`, whose detail names the calendar and counts its events: `"Household": 10 events from today to 14 days ahead`. Unknown zones come back as warnings. There's no version.

## Repairs and errors

| What happened | Error | Field |
|---|---|---|
| 401 or 403 | `auth` Repair: the address was refused, copy it again | `feed_url` |
| 404 or 410 | `api_changed` Repair: the address was reset or the calendar deleted | `feed_url` |
| A 2xx that isn't iCal (a sign-in page, HTML, a broken feed) | `api_changed` Repair: "that address isn't an iCal feed" | `feed_url` |
| Other statuses | transient, "the feed answered 500 Internal Server Error" | |
| Can't connect, DNS, timeout, TLS | the framework's classes, worded with the host only | |

## Limits

- One calendar per instance. Add another for a second calendar.
- Two weeks, 60 events. A busier calendar shows its soonest 60.
- `RECURRENCE-ID;RANGE=THISANDFUTURE` is read as a single instance's override. A rule is followed for at most 100,000 steps from its first instance, so a `SECONDLY` or `MINUTELY` rule started long ago may show nothing.
- Without the zone database, a zone named only by a Windows name or IANA name and missing from the feed's `VTIMEZONE`s is read as UTC (with the warning).
- Read-only: nothing is ever written back.

## Fixtures

Synthetic, in [`testdata/integrations/ical/`](../../testdata/integrations/ical/README.md).
