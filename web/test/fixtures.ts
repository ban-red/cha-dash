/**
 * The simulator's golden snapshots as realtime topic documents.
 *
 * `graph` comes in two shapes: the simulator's own (health as an object) and
 * internal/graph's (health as a string, reason and since in attrs), so the UI
 * is tested against both.
 */
import healthyRaw from '../../sim/snapshots/healthy.json?raw';
import incidentsRaw from '../../sim/snapshots/incidents.json?raw';
import timelineRaw from '../../sim/snapshots/timeline.json?raw';
import type { GraphDoc, Incident, InboxDoc, MetricsDoc, Resource, Series } from '../src/model/types';

interface SimResource extends Omit<Resource, 'health'> {
  health: { status: string; reason?: string; since?: string };
}

interface SimState {
  now: string;
  seq: number;
  history: { start: string; end: string; step_s: number; points: number };
  resources: SimResource[];
  relations: { from: string; type: string; to: string }[];
  series: (Series & { resource: string; metric: string })[];
  incidents: Incident[];
}

export interface Timeline {
  events: { seq: number; at: string; type: string; incident?: Incident; resource?: SimResource; id?: string }[];
}

export const healthy = JSON.parse(healthyRaw) as SimState;
export const withIncidents = JSON.parse(incidentsRaw) as SimState;
export const timeline = JSON.parse(timelineRaw) as Timeline;

export function graphDoc(s: SimState, shape: 'sim' | 'hub' = 'sim'): GraphDoc {
  const resources: Record<string, Resource> = {};
  for (const r of s.resources) {
    if (shape === 'sim') {
      resources[r.id] = structuredClone(r) as Resource;
    } else {
      const { health, ...rest } = structuredClone(r);
      const attrs: Record<string, unknown> = { ...(rest.attrs ?? {}) };
      if (health.reason) attrs['health.reason'] = health.reason;
      if (health.since) attrs['health.since'] = health.since;
      resources[r.id] = { ...rest, health: health.status, attrs };
    }
  }
  const relations: GraphDoc['relations'] = {};
  for (const rel of s.relations) relations[`${rel.from} ${rel.type} ${rel.to}`] = { ...rel };
  return { resources, relations };
}

export function metricsDoc(s: SimState): MetricsDoc {
  const series: Record<string, Series> = {};
  for (const x of s.series) series[`${x.resource}#${x.metric}`] = structuredClone(x);
  return { series };
}

export function inboxDoc(s: SimState): InboxDoc {
  const incidents: Record<string, Incident> = {};
  for (const i of s.incidents) incidents[i.id] = structuredClone(i);
  return { now: s.now, incidents };
}

export function docs(s: SimState, shape: 'sim' | 'hub' = 'sim') {
  return { graph: graphDoc(s, shape), metrics: metricsDoc(s), inbox: inboxDoc(s) };
}

/** The incident as the timeline has it when it resolves. */
export function resolvedIncident(id: string): Incident {
  const ev = timeline.events.find((e) => e.type === 'incident.resolve' && e.incident?.id === id);
  if (!ev?.incident) throw new Error(`no resolve event for ${id}`);
  return structuredClone(ev.incident);
}
