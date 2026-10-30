/** History as CSV, for Download CSV on a sheet's Metrics tab. */
import { describe, expect, it } from 'vitest';
import { csvName, historyCsv, type HistoryReply } from '../src/model/history';

describe('historyCsv', () => {
  it('writes a row a point with each series’ average, low and high, empty for a gap', () => {
    const start = Date.parse('2026-10-03T00:00:00Z');
    const reply: HistoryReply = {
      level: 'standard',
      series: {
        'vm:a#cpu.pct': { unit: '%', start, step: 900_000, avg: [1.5, null], min: [1, null], max: [2, null] },
        'vm:a#mem.mib': { unit: 'MiB', start: start + 900_000, step: 900_000, avg: [512], min: [500], max: [520] },
      },
    };
    expect(historyCsv(reply, { 'vm:a#cpu.pct': 'CPU' }).split('\r\n')).toEqual([
      'time,CPU avg (%),CPU min (%),CPU max (%),vm:a#mem.mib avg (MiB),vm:a#mem.mib min (MiB),vm:a#mem.mib max (MiB)',
      '2026-10-03T00:00:00.000Z,1.5,1,2,,,',
      '2026-10-03T00:15:00.000Z,,,,512,500,520',
      '',
    ]);
  });

  it('quotes commas and quotes, and keeps names from running as formulas', () => {
    const reply: HistoryReply = { level: 'standard', series: { k: { unit: '', start: 0, step: 60_000, avg: [-3], min: [-3], max: [-3] } } };
    expect(historyCsv(reply, { k: '=SUM(A1),"x"' }).split('\r\n')[0]).toBe(`time,"'=SUM(A1),""x"" avg","'=SUM(A1),""x"" min","'=SUM(A1),""x"" max"`);
    expect(historyCsv(reply).split('\r\n')[1]).toBe('1970-01-01T00:00:00.000Z,-3,-3,-3');
  });

  it('names the file after the resource, the range and the day', () => {
    expect(csvName('Plex Media Server', '24h', Date.parse('2026-10-29T12:00:00Z'))).toBe('plex-media-server-24h-2026-10-29.csv');
    expect(csvName('—', '7d', 0)).toBe('history-7d-1970-01-01.csv');
  });
});
