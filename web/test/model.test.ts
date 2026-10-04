import { describe, expect, it } from 'vitest';
import { calendar, engines, hostRows, lastBackup, placeOf, value } from '../src/model/graph';
import { incidentKicker, openProblems, openRepairs, resolved, settling } from '../src/model/inbox';
import { normalizeHealth } from '../src/model/types';
import { docs, healthy, withIncidents } from './fixtures';

describe('graph views over the Demo Home', () => {
  for (const shape of ['sim', 'hub'] as const) {
    it(`reads health from the ${shape} shape`, () => {
      const { graph } = docs(withIncidents, shape);
      const h = normalizeHealth(graph.resources['pve:jupiter-nas']);
      expect(h).toEqual({ status: 'attention', reason: 'memory 95% · no swap', since: '2026-10-03T13:30:00Z' });
      expect(normalizeHealth(graph.resources['integration:plex']).status).toBe('repair');
      expect(normalizeHealth(undefined).status).toBe('unknown');
    });
  }

  it('lists hosts with live metrics and guests', () => {
    const { graph, metrics } = docs(withIncidents);
    const rows = hostRows(graph, metrics);
    expect(rows.map((r) => r.name)).toEqual(['io', 'jupiter', 'jupiter-nas', 'mars-1', 'mars-2', 'minig5', 'pluto']);
    const nas = rows.find((r) => r.name === 'jupiter-nas')!;
    expect(nas.mem).toBeCloseTo(95.3);
    expect(nas.health.status).toBe('attention');
    expect(nas.role).toBe('NAS');
    expect([nas.guestsRunning, nas.guestsTotal]).toEqual([1, 2]);
    const io = rows.find((r) => r.name === 'io')!;
    expect([io.guestsRunning, io.guestsTotal]).toEqual([1, 3]);
    expect(rows.find((r) => r.name === 'jupiter')!.engines).toBe(1);
  });

  it('summarises Docker engines', () => {
    const { graph } = docs(withIncidents);
    const e = engines(graph);
    expect(e.map((x) => [x.name, x.total])).toEqual([
      ['jupiter', 11],
      ['pvedocker', 9],
    ]);
    expect(e[0]!.unhealthy.map((c) => c.name)).toEqual(['binhex-qbittorrentvpn']);
  });

  it('places services the way people say it', () => {
    const { graph } = docs(withIncidents);
    expect(placeOf(graph, 'app:sonarr')).toBe('jupiter');
    expect(placeOf(graph, 'app:homeassistant')).toBe('minig5');
    expect(placeOf(graph, 'app:traefik')).toBe('pvedocker');
    expect(placeOf(graph, 'net:opnsense')).toBe('pluto');
  });

  it('keeps unknown as null, never 0', () => {
    const { metrics } = docs(withIncidents);
    expect(value(metrics, 'app:plex', 'streams')).toBe(0);
    expect(metrics.series['app:plex#streams']?.stale).toBe(true);
    expect(value(metrics, 'pve:io/qemu/201', 'cpu.pct')).toBeNull(); // stopped VM: no metrics
    expect(value(undefined, 'x', 'y')).toBeNull();
  });

  it('finds the next releases and the last backup', () => {
    const { graph } = docs(withIncidents);
    const next = calendar(graph, Date.parse(withIncidents.now));
    expect(next[0]).toMatchObject({ title: 'Black Clover', source: 'Sonarr' });
    // io's run ends last (02:55); pluto's two guests, mars's one, homeassistant and win-gaming failed.
    expect(lastBackup(graph)?.at).toBe('2026-10-03T02:55:00Z');
    expect(lastBackup(graph)?.failed).toBe(5);
  });
});

describe('inbox views', () => {
  it('splits problems, repairs and settling incidents at 14:05', () => {
    const { inbox, graph } = docs(withIncidents);
    const problems = openProblems(inbox);
    // Down first (scratch in internal-error), then by age.
    expect(problems.map((i) => i.id)).toEqual([
      'proxmox-jupiter-nas.guest.101',
      'proxmox-pluto.backup.daily',
      'backup-failed',
      'proxmox-mars.backup.daily',
      'proxmox-io.backup.daily',
      'pbs-datastore-filling',
      'host-memory-pressure',
      'qbittorrent-oom',
    ]);
    expect(openRepairs(inbox).map((i) => i.id)).toEqual(['karakeep-api-changed', 'unraid-jupiter.repair', 'plex-token-revoked']);
    // Nothing is settling by itself at 14:05; the standing hints aren't settling, they're hints.
    expect(settling(inbox)).toEqual([]);
    expect(incidentKicker(problems[0]!, graph)).toBe('VM · scratch');
    expect(incidentKicker(problems[1]!, graph)).toBe('Backups');
    expect(incidentKicker(problems[5]!, graph)).toBe('Backups');
    expect(incidentKicker(problems[6]!, graph)).toBe('Host · jupiter‑nas');
  });

  it('puts down before attention', () => {
    const { inbox } = docs(withIncidents);
    inbox.incidents['host-memory-pressure']!.severity = 'down';
    expect(openProblems(inbox)[0]!.id).toBe('proxmox-jupiter-nas.guest.101'); // down since 11:20
    expect(openProblems(inbox)[1]!.id).toBe('host-memory-pressure');
  });

  it('is empty when the home is quiet', () => {
    const { inbox } = docs(healthy);
    expect(openProblems(inbox)).toEqual([]);
    expect(openRepairs(inbox)).toEqual([]);
    expect(resolved(inbox)).toEqual([]);
  });
});
