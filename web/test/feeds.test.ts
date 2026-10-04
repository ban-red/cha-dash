import { describe, expect, it } from 'vitest';
import { bindKind, decodeBinding, encodeBinding, feedFor, serviceBindingGroups } from '../src/model/feeds';
import { WIDGETS } from '../src/model/widgets';
import { docs, healthy } from './fixtures';

const graph = docs(healthy).graph;
const now = Date.parse(healthy.now);

describe('what the graph can feed', () => {
  it('reads every catalog entry, with the rules for types fed by another kind', () => {
    const fed = Object.fromEntries(Object.values(WIDGETS).map((w) => [w.type, feedFor(w, graph, now)]));
    for (const t of ['fleet', 'cameras', 'opnsense', 'backups', 'containers', 'downloads', 'calendar', 'incidents', 'service', 'plex', 'traefik', 'uptimekuma', 'homeassistant']) {
      expect([t, fed[t]!.available]).toEqual([t, true]);
    }
    expect(fed['arr']!.candidates.map((r) => r.id).sort()).toEqual(['app:radarr', 'app:sonarr']);
    expect(fed['host']!.candidates.every((r) => r.kind === 'host')).toBe(true);
    expect(fed['host']!.candidates.length).toBeGreaterThan(1);
  });

  it('feeds a type it has no rule for by its own kind or app type', () => {
    expect(feedFor({ type: 'immich' }, graph, now).available).toBe(true); // app:immich
    expect(feedFor({ type: 'nothing-like-it' }, graph, now).available).toBe(false);
    const vms = feedFor({ type: 'vm', bound: true }, graph, now);
    expect(vms.available).toBe(true);
    expect(vms.candidates.every((r) => r.kind === 'vm')).toBe(true);
    expect(feedFor({ type: 'nothing-like-it', bound: true }, graph, now)).toEqual({ available: false, candidates: [] });
  });

  it('counts singletons as available before the graph arrives, bound widgets not', () => {
    expect(feedFor(WIDGETS['plex']!, undefined)).toEqual({ available: true, candidates: [] });
    expect(feedFor(WIDGETS['arr']!, undefined)).toEqual({ available: false, candidates: [] });
    expect(feedFor(WIDGETS['service']!, undefined).available).toBe(true);
    expect([bindKind(WIDGETS['service']!), bindKind(WIDGETS['arr']!), bindKind(WIDGETS['fleet']!)]).toEqual(['service', 'resource', 'none']);
  });

  it('offers a service tile checks, integrations and services to take its status from', () => {
    const groups = serviceBindingGroups(graph);
    expect(groups.map((g) => g.label)).toEqual(expect.arrayContaining(['Integrations', 'Services and containers']));
    const integ = groups.find((g) => g.label === 'Integrations')!.choices;
    expect(integ.map((c) => c.value)).toContain('instance:sonarr');
    for (const b of [{ check: 'Plex' }, { instance: 'i_x' }, { resource: 'docker:i_d/container/a:b' }]) expect(decodeBinding(encodeBinding(b))).toEqual(b);
    expect(encodeBinding(undefined)).toBe('');
    expect(decodeBinding('')).toBeUndefined();
    expect(decodeBinding('bogus:x')).toBeUndefined();
  });
});
