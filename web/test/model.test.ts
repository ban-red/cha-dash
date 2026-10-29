import { describe, expect, it } from 'vitest';
import { calendar, engines, hostRows, lastBackup, placeOf, value } from '../src/model/graph';
import { incidentKicker, openProblems, openRepairs, resolved, settling } from '../src/model/inbox';
import { normalizeHealth } from '../src/model/types';
import { docs, healthy, withIncidents } from './fixtures';

describe('graph views over the Demo Home', () => {
  for (const shape of ['sim', 'hub'] as const) {
    it(`reads health from the ${shape} shape`, () => {
      const { graph } = docs(withIncidents, shape);
      const h = normalizeHealth(graph.resources['pve:stork']);
      expect(h).toEqual({ status: 'attention', reason: 'memory 95% · no swap', since: '2026-10-03T13:30:00Z' });
      expect(normalizeHealth(graph.resources['integration:plex']).status).toBe('repair');
      expect(normalizeHealth(undefined).status).toBe('unknown');
    });
  }

  it('lists hosts with live metrics and guests', () => {
    const { graph, metrics } = docs(withIncidents);
    const rows = hostRows(graph, metrics);
    expect(rows.map((r) => r.name)).toEqual(['egret', 'heron', 'magpie', 'owl', 'stork', 'wren']);
    const nas = rows.find((r) => r.name === 'stork')!;
    expect(nas.mem).toBeCloseTo(95);
    expect(nas.health.status).toBe('attention');
    expect(nas.role).toBe('NAS');
    expect([nas.guestsRunning, nas.guestsTotal]).toEqual([1, 2]);
    const owl = rows.find((r) => r.name === 'owl')!;
    expect([owl.guestsRunning, owl.guestsTotal]).toEqual([1, 3]);
    expect(rows.find((r) => r.name === 'magpie')!.engines).toBe(1);
  });

  it('summarises Docker engines', () => {
    const { graph } = docs(withIncidents);
    const e = engines(graph);
    expect(e.map((x) => [x.name, x.total])).toEqual([
      ['magpie', 14],
      ['rook', 15],
    ]);
    expect(e[0]!.unhealthy.map((c) => c.name)).toEqual(['qbittorrent']);
  });

  it('places services the way people say it', () => {
    const { graph } = docs(withIncidents);
    expect(placeOf(graph, 'app:sonarr')).toBe('magpie');
    expect(placeOf(graph, 'app:homeassistant')).toBe('heron');
    expect(placeOf(graph, 'app:traefik')).toBe('rook');
    expect(placeOf(graph, 'net:opnsense')).toBe('wren');
  });

  it('keeps unknown as null, never 0', () => {
    const { metrics } = docs(withIncidents);
    expect(value(metrics, 'app:plex', 'streams')).toBe(0);
    expect(metrics.series['app:plex#streams']?.stale).toBe(true);
    expect(value(metrics, 'pve:owl/qemu/301', 'cpu.pct')).toBeNull(); // stopped VM: no metrics
    expect(value(undefined, 'x', 'y')).toBeNull();
  });

  it('finds the next releases and the last backup', () => {
    const { graph } = docs(withIncidents);
    const next = calendar(graph, Date.parse(withIncidents.now));
    expect(next[0]).toMatchObject({ title: 'Starling Point', source: 'Sonarr' });
    // owl's run ends last (02:55); wren's three guests, homeassistant and win-games failed.
    expect(lastBackup(graph)?.at).toBe('2026-10-03T02:55:00Z');
    expect(lastBackup(graph)?.failed).toBe(5);
  });
});

describe('inbox views', () => {
  it('splits problems, repairs and settling incidents at 14:05', () => {
    const { inbox, graph } = docs(withIncidents);
    const problems = openProblems(inbox);
    // Down first (Nextcloud in maintenance mode, sandbox in internal-error), then by age.
    expect(problems.map((i) => i.id)).toEqual([
      'nextcloud-maintenance',
      'proxmox-aviary.guest.221',
      'proxmox-wren.backup.daily',
      'backup-failed',
      'proxmox-owl.backup.daily',
      'pbs-datastore-filling',
      'speedtest-slow',
      'host-memory-pressure',
      'qbittorrent-oom',
    ]);
    expect(openRepairs(inbox).map((i) => i.id)).toEqual(['karakeep-api-changed', 'unraid-magpie.repair', 'plex-token-revoked']);
    // Nothing is settling by itself at 14:05; the standing hints aren't settling, they're hints.
    expect(settling(inbox)).toEqual([]);
    expect(incidentKicker(problems[1]!, graph)).toBe('VM · sandbox');
    expect(incidentKicker(problems[2]!, graph)).toBe('Backups');
    expect(incidentKicker(problems[5]!, graph)).toBe('Backups');
    expect(incidentKicker(problems[7]!, graph)).toBe('Host · stork');
  });

  it('puts down before attention', () => {
    const { inbox } = docs(withIncidents);
    inbox.incidents['host-memory-pressure']!.severity = 'down';
    expect(openProblems(inbox)[0]!.id).toBe('nextcloud-maintenance'); // down since 07:10
    expect(openProblems(inbox)[1]!.id).toBe('proxmox-aviary.guest.221'); // down since 11:20
    expect(openProblems(inbox)[2]!.id).toBe('host-memory-pressure');
  });

  it('is empty when the home is quiet', () => {
    const { inbox } = docs(healthy);
    expect(openProblems(inbox)).toEqual([]);
    expect(openRepairs(inbox)).toEqual([]);
    expect(resolved(inbox)).toEqual([]);
  });
});
