/** The Calendar widget's event model (model/agenda.ts): days in the display zone, all-day ends, ordering. */
import { afterEach, describe, expect, it } from 'vitest';
import { setDisplayTimeZone } from '../src/lib/format';
import { addDays, dayKey, dayStart, daysBetween, eventsOf, onDay, weekday } from '../src/model/agenda';
import type { Resource } from '../src/model/types';

const cal = (events: unknown[], id = 'ical:i_x/calendar/main'): Resource => ({ id, kind: 'calendar', name: 'Work', attrs: { type: 'calendar', events } });

afterEach(() => setDisplayTimeZone(undefined));

describe('days', () => {
  it('keys, moves and counts days', () => {
    setDisplayTimeZone('UTC');
    expect(dayKey(Date.parse('2026-10-03T23:59:00Z'))).toBe('2026-10-03');
    expect(addDays('2026-10-31', 1)).toBe('2026-11-01');
    expect(addDays('2026-03-01', -1)).toBe('2026-02-28');
    expect(daysBetween('2026-10-03', '2026-10-08')).toBe(5);
    expect(weekday('2026-10-05')).toBe(0); // a Monday
    expect(weekday('2026-10-04')).toBe(6);
  });

  it('finds where a day starts, far east and across a DST change', () => {
    setDisplayTimeZone('Pacific/Kiritimati'); // UTC+14: noon UTC is already the next day there
    expect(new Date(dayStart('2026-10-03')).toISOString()).toBe('2026-10-02T10:00:00.000Z');
    setDisplayTimeZone('Europe/London'); // clocks go back on 25 Oct 2026
    expect(new Date(dayStart('2026-10-25')).toISOString()).toBe('2026-10-24T23:00:00.000Z');
    expect(new Date(dayStart('2026-10-26')).toISOString()).toBe('2026-10-26T00:00:00.000Z');
  });
});

describe('events', () => {
  it('reads all-day ends as exclusive, and a timed event ending at midnight as the day before', () => {
    setDisplayTimeZone('UTC');
    const ev = eventsOf([
      cal([
        { start: '2026-10-03T20:00:00Z', end: '2026-10-04T00:00:00Z', all_day: false, title: 'Party' },
        { start: '2026-10-05', end: '2026-10-08', all_day: true, title: 'Lake trip' },
        { start: 'not a time', title: 'Broken' },
        { start: '2026-10-04', title: 'No end' },
      ]),
    ]);
    expect(ev.map((e) => [e.title, e.startDay, e.endDay])).toEqual([
      ['Party', '2026-10-03', '2026-10-03'],
      ['No end', '2026-10-04', '2026-10-04'],
      ['Lake trip', '2026-10-05', '2026-10-07'],
    ]);
  });

  it('lists a day with all-day and continuing events first, then by start', () => {
    setDisplayTimeZone('UTC');
    const ev = eventsOf([
      cal([
        { start: '2026-10-06T09:00:00Z', end: '2026-10-06T10:00:00Z', title: 'Standup' },
        { start: '2026-10-05T20:00:00Z', end: '2026-10-06T08:00:00Z', title: 'Night shift' },
        { start: '2026-10-05', end: '2026-10-08', all_day: true, title: 'Lake trip' },
      ]),
    ]);
    expect(onDay(ev, '2026-10-06').map((e) => e.title)).toEqual(['Lake trip', 'Night shift', 'Standup']);
    expect(onDay(ev, '2026-10-08')).toEqual([]);
  });
});
