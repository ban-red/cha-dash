/**
 * The wall display's calm logic (lib/display.ts): a pure function of the
 * time, the incidents, the connection and the display's quiet hours. Every
 * case runs on a fixed clock; no timers.
 */
import { describe, expect, it } from 'vitest';
import { calm, changeKey, hubMinutes, inQuiet, isNight, shiftAt, staleWords, type CalmInput } from '../src/lib/display';
import type { GraphDoc, Incident } from '../src/model/types';

const MIN = 60_000;
/** 2026-11-06 at hh:mm UTC. */
const utc = (hh: number, mm = 0, day = 6) => Date.UTC(2026, 10, day, hh, mm);

function incident(id: string, over: Partial<Incident> = {}): Incident {
  return { id, class: 'problem', severity: 'attention', status: 'open', title: `${id} is unhappy`, summary: '', target: `app:${id}`, opened_at: '2026-11-06T12:00:00Z', ...over };
}

function input(over: Partial<CalmInput> = {}): CalmInput {
  return { now: utc(12), zone: '', offsetMinutes: 0, incidents: [], seen: new Set(), lastChange: utc(12), live: true, lastDataAt: utc(12), quiet: null, ...over };
}

describe('hub local time', () => {
  it('uses the zone when the hub names one', () => {
    // Berlin is UTC+1 in November.
    expect(hubMinutes(utc(20, 30), 'Europe/Berlin', 0)).toBe(21 * 60 + 30);
    // In July it's UTC+2: Intl knows the daylight saving rule.
    expect(hubMinutes(Date.UTC(2026, 6, 1, 20, 30), 'Europe/Berlin', 0)).toBe(22 * 60 + 30);
  });

  it('falls back on the fixed offset when there is no zone', () => {
    expect(hubMinutes(utc(20, 30), '', 60)).toBe(21 * 60 + 30);
    expect(hubMinutes(utc(23, 30), '', 90)).toBe(60); // past midnight: the next day's 01:00
    expect(hubMinutes(utc(0, 30), '', -60)).toBe(23 * 60 + 30); // before midnight: the day before
  });

  it('falls back on the offset when the zone is one Intl does not know', () => {
    expect(hubMinutes(utc(12), 'Not/AZone', 120)).toBe(14 * 60);
  });
});

describe('night tint', () => {
  it('is on from 21:00 to 07:00 hub time', () => {
    expect(isNight(20 * 60 + 59)).toBe(false);
    expect(isNight(21 * 60)).toBe(true);
    expect(isNight(0)).toBe(true);
    expect(isNight(6 * 60 + 59)).toBe(true);
    expect(isNight(7 * 60)).toBe(false);
    expect(calm(input({ now: utc(21, 5), lastChange: utc(21, 5) })).tint).toBe(true);
    expect(calm(input({ now: utc(12) })).tint).toBe(false);
    // In Berlin, 20:30 UTC is 21:30: tinted.
    expect(calm(input({ now: utc(20, 30), lastChange: utc(20, 30), zone: 'Europe/Berlin' })).tint).toBe(true);
  });
});

describe('quiet hours', () => {
  it('span midnight', () => {
    const q = { start: '22:00', end: '07:00' };
    expect(inQuiet(21 * 60 + 59, q)).toBe(false);
    expect(inQuiet(22 * 60, q)).toBe(true);
    expect(inQuiet(3 * 60, q)).toBe(true);
    expect(inQuiet(7 * 60, q)).toBe(false);
  });

  it('within one day', () => {
    const q = { start: '13:00', end: '14:30' };
    expect(inQuiet(12 * 60 + 59, q)).toBe(false);
    expect(inQuiet(13 * 60, q)).toBe(true);
    expect(inQuiet(14 * 60 + 29, q)).toBe(true);
    expect(inQuiet(14 * 60 + 30, q)).toBe(false);
  });

  it('none, equal ends, or a malformed time are never quiet', () => {
    expect(inQuiet(3 * 60, null)).toBe(false);
    expect(inQuiet(3 * 60, { start: '07:00', end: '07:00' })).toBe(false);
    expect(inQuiet(3 * 60, { start: 'late', end: '07:00' })).toBe(false);
  });

  it('go dark, and a change does not light them', () => {
    const quiet = { start: '22:00', end: '07:00' };
    const c = calm(input({ now: utc(23), lastChange: utc(23), quiet }));
    expect(c.brightness).toBe('dark');
    expect(c.quiet).toBe(true);
    // The same moment outside quiet hours is full brightness.
    expect(calm(input({ now: utc(23), lastChange: utc(23) })).brightness).toBe('full');
  });

  it('read the hub zone across a day boundary', () => {
    const quiet = { start: '22:00', end: '07:00' };
    // 21:30 UTC is 22:30 in Berlin: quiet. 05:30 UTC is 06:30: still quiet; 06:30 UTC is 07:30: not.
    expect(calm(input({ now: utc(21, 30), zone: 'Europe/Berlin', quiet })).brightness).toBe('dark');
    expect(calm(input({ now: utc(5, 30, 7), lastChange: utc(5, 30, 7), zone: 'Europe/Berlin', quiet })).brightness).toBe('dark');
    expect(calm(input({ now: utc(6, 30, 7), lastChange: utc(6, 30, 7), zone: 'Europe/Berlin', quiet })).brightness).toBe('full');
  });

  it('wake only for an unacknowledged down incident', () => {
    const quiet = { start: '22:00', end: '07:00' };
    const now = utc(2);
    const attn = calm(input({ now, lastChange: utc(1), quiet, incidents: [incident('nas', { severity: 'attention' })] }));
    expect(attn.brightness).toBe('dark');
    expect(attn.wake).toBeNull();
    const down = calm(input({ now, lastChange: utc(1), quiet, incidents: [incident('nas', { severity: 'down' })] }));
    expect(down.brightness).toBe('full');
    expect(down.wake).toMatchObject({ id: 'nas', severity: 'down', text: 'nas is unhappy' });
    const acked = calm(input({ now, quiet, incidents: [incident('nas', { severity: 'down', acknowledged: { by: 'sam', at: '2026-11-06T01:00:00Z' } })] }));
    expect(acked.brightness).toBe('dark');
    expect(acked.wake).toBeNull();
  });
});

describe('brightness', () => {
  it('is full for 2 minutes after a change, then dims', () => {
    expect(calm(input({ now: utc(12), lastChange: utc(12) })).brightness).toBe('full');
    expect(calm(input({ now: utc(12, 1), lastChange: utc(12) })).brightness).toBe('full');
    expect(calm(input({ now: utc(12) + 2 * MIN - 1, lastChange: utc(12) })).brightness).toBe('full');
    expect(calm(input({ now: utc(12, 2), lastChange: utc(12) })).brightness).toBe('dim');
    expect(calm(input({ now: utc(18), lastChange: utc(12) })).brightness).toBe('dim');
  });
});

describe('waking', () => {
  const later = { now: utc(15), lastChange: utc(12) };

  it('a new unacknowledged attention or down problem wakes it with its one line', () => {
    const c = calm(input({ ...later, incidents: [incident('plex')] }));
    expect(c.brightness).toBe('full');
    expect(c.wake).toEqual({ id: 'plex', severity: 'attention', text: 'plex is unhappy' });
  });

  it('down beats attention, then the newest', () => {
    const c = calm(
      input({
        ...later,
        incidents: [
          incident('a', { severity: 'attention', opened_at: '2026-11-06T14:00:00Z' }),
          incident('b', { severity: 'down', opened_at: '2026-11-06T13:00:00Z' }),
          incident('c', { severity: 'down', opened_at: '2026-11-06T14:30:00Z' }),
        ],
      }),
    );
    expect(c.wake?.id).toBe('c');
  });

  it('an incident the page saw at first does not wake it', () => {
    const c = calm(input({ ...later, incidents: [incident('plex')], seen: new Set(['plex']) }));
    expect(c.wake).toBeNull();
    expect(c.brightness).toBe('dim');
  });

  it('info, resolved, repairs and suggestions never wake it', () => {
    const c = calm(
      input({
        ...later,
        incidents: [
          incident('i', { severity: 'info' }),
          incident('r', { status: 'resolved', severity: 'down' }),
          incident('k', { class: 'repair', severity: 'down' }),
          incident('s', { class: 'suggestion', severity: 'attention' }),
        ],
      }),
    );
    expect(c.wake).toBeNull();
    expect(c.brightness).toBe('dim');
  });

  it('dims again once the incident resolves or is acknowledged', () => {
    const open = calm(input({ ...later, incidents: [incident('plex')] }));
    expect(open.brightness).toBe('full');
    const resolved = calm(input({ ...later, now: utc(15, 5), lastChange: utc(15), incidents: [incident('plex', { status: 'resolved' })] }));
    expect(resolved.wake).toBeNull();
    expect(resolved.brightness).toBe('dim');
    const acked = calm(input({ ...later, incidents: [incident('plex', { acknowledged: { by: 'sam', at: '2026-11-06T14:59:00Z' } })] }));
    expect(acked.wake).toBeNull();
    expect(acked.brightness).toBe('dim');
  });

  it('acknowledged incidents are listed with who is on them', () => {
    const c = calm(input({ ...later, incidents: [incident('plex', { severity: 'down', acknowledged: { by: 'sam', at: '2026-11-06T14:59:00Z' } }), incident('nas', { severity: 'info', acknowledged: { by: 'kim', at: 'x' } })] }));
    expect(c.acknowledged).toEqual([{ id: 'plex', severity: 'down', text: 'plex is unhappy', by: 'sam' }]);
  });
});

describe('burn-in shift', () => {
  it('moves a few pixels every 3 minutes, the same for the same moment', () => {
    const t = utc(12);
    expect(shiftAt(t)).toEqual(shiftAt(t + 2 * MIN));
    const seen = new Set<string>();
    for (let i = 0; i < 8; i++) {
      const s = shiftAt(t + i * 3 * MIN);
      expect(Math.abs(s.x)).toBeLessThanOrEqual(4);
      expect(Math.abs(s.y)).toBeLessThanOrEqual(4);
      seen.add(`${s.x},${s.y}`);
    }
    expect(seen.size).toBe(8);
    expect(shiftAt(t + 3 * MIN)).not.toEqual(shiftAt(t));
    expect(calm(input({ now: t })).shift).toEqual(shiftAt(t));
  });
});

describe('stale data', () => {
  it('is null while the socket is live', () => {
    expect(calm(input({ live: true, lastDataAt: utc(11) })).stale).toBeNull();
  });

  it('says how old the data is when the socket is down', () => {
    expect(calm(input({ live: false, now: utc(12), lastDataAt: utc(12) - 20_000 })).stale).toEqual({ ageMs: 20_000, text: 'Data from less than a minute ago' });
    expect(calm(input({ live: false, now: utc(12), lastDataAt: utc(11, 59) })).stale?.text).toBe('Data from 1 minute ago');
    expect(calm(input({ live: false, now: utc(12), lastDataAt: utc(11, 48) })).stale?.text).toBe('Data from 12 minutes ago');
    expect(calm(input({ live: false, now: utc(15), lastDataAt: utc(12) })).stale?.text).toBe('Data from 3 hours ago');
    expect(calm(input({ live: false, now: utc(13), lastDataAt: utc(12) })).stale?.text).toBe('Data from 1 hour ago');
    expect(staleWords(3 * 86_400_000)).toBe('Data from 3 days ago');
  });

  it('says it is waiting when nothing ever arrived', () => {
    expect(calm(input({ live: false, lastDataAt: 0 })).stale).toEqual({ ageMs: 0, text: 'Waiting for the hub' });
  });

  it('stays visible in quiet hours', () => {
    const c = calm(input({ now: utc(23), live: false, lastDataAt: utc(22), quiet: { start: '22:00', end: '07:00' } }));
    expect(c.brightness).toBe('dark');
    expect(c.stale?.text).toBe('Data from 1 hour ago');
  });
});

describe('what counts as a change', () => {
  it('ignores metrics and order, but not health or incidents', () => {
    const graph: GraphDoc = { resources: { a: { id: 'a', kind: 'app', health: 'ok' }, b: { id: 'b', kind: 'app', health: { status: 'down' } } }, relations: {} };
    const inbox = { now: '', incidents: { x: incident('x') } };
    const k = changeKey(graph, inbox);
    expect(changeKey({ resources: { b: graph.resources['b']!, a: graph.resources['a']! }, relations: {} }, { now: 'later', incidents: { x: incident('x') } })).toBe(k);
    expect(changeKey({ ...graph, resources: { ...graph.resources, a: { id: 'a', kind: 'app', health: 'attention' } } }, inbox)).not.toBe(k);
    expect(changeKey(graph, { now: '', incidents: { x: incident('x', { acknowledged: { by: 'sam', at: 't' } }) } })).not.toBe(k);
    expect(changeKey(graph, { now: '', incidents: { x: incident('x', { status: 'resolved' }) } })).not.toBe(k);
    expect(changeKey(undefined, undefined)).toBe('');
  });
});
