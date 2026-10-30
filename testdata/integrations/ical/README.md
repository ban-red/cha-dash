# Calendar feed (iCal) fixtures (synthetic)

**Synthetic.** Nothing here was fetched from Google, Microsoft or anyone else. The feeds were written by hand for the iCal integration's contract tests ([`integrations/ical`](../../../integrations/ical/README.md)), in the shape those providers publish: Google's `PRODID`, `X-WR-CALNAME`, `X-WR-TIMEZONE` and `VTIMEZONE`s with `X-LIC-LOCATION`; Exchange's Windows zone names, `VTIMEZONE`s starting in 1601 and `X-MICROSOFT-*` properties. Lines end in CRLF, and some are folded. Titles and places are made up; addresses use `*.example`; there are no real names.

The tests serve both at a Google-like path, `/calendar/ical/owner.group.example/private-0123…/basic.ics`, whose fake token they check never leaves the instance. The harness's clock is 2026-10-03 14:05 UTC.

| File | Shape | What's in it |
|---|---|---|
| `google.ics` | Google Calendar's secret iCal address | `Household`, Europe/London (UTC+1 until 25 October). A weekly event on Mondays and Thursdays with one instance moved (`RECURRENCE-ID`), one cancelled (`STATUS:CANCELLED` override) and one removed (`EXDATE`); a weekly event ending by a UTC `UNTIL`; one-day, three-day and yearly all-day events, one begun before today; a UTC event with an escaped comma in its place; a New York event with an organiser, an attendee, a description and a URL (none of which may be published); a floating time; and events yesterday, after the window and cancelled, which don't appear |
| `outlook.ics` | Outlook's published calendar | `Calendar`, with Windows `TZID`s: `W. Europe Standard Time` (Europe/Berlin, the calendar's zone) and `Pacific Standard Time`, each with its `VTIMEZONE`; a weekly event with an `EXDATE`; an all-day event; an unescaped comma in a title; and `Mars Standard Time`, a zone with no `VTIMEZONE`, read as UTC with a warning |

The tests build smaller feeds inline for the 60-event cap and the rarer shapes (`DURATION`, `RDATE` lists and periods, custom zones, lone overrides).
