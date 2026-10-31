/**
 * The time format setting (lib/format.ts): 24-hour by default, 12-hour on
 * request, remembered on this device, and reflected by hhmm() and absolute().
 */
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { absolute, hhmm, setTimeFormat } from '../src/lib/format';

const AT = '2026-10-03T14:05:10Z';

beforeEach(() => localStorage.clear());
afterEach(() => setTimeFormat('24h'));

describe('time format', () => {
  it('shows 24-hour time by default', () => {
    expect(hhmm(AT)).toMatch(/^\d\d:\d\d$/);
  });

  it('switches to 12-hour and back', () => {
    setTimeFormat('12h');
    expect(hhmm(AT)).toMatch(/^\d{1,2}:\d\d\s?[ap]m$/i);
    expect(absolute(AT)).toMatch(/[ap]m/i);
    setTimeFormat('24h');
    expect(hhmm(AT)).toMatch(/^\d\d:\d\d$/);
    expect(absolute(AT)).not.toMatch(/[ap]m/i);
  });

  it('remembers the choice', () => {
    setTimeFormat('12h');
    expect(localStorage.getItem('chadash.timeformat')).toBe('12h');
  });
});
