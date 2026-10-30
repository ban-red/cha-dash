/**
 * Home Assistant's lists, read from the app's attrs (integrations/
 * homeassistant), for the tile (HomeAssistantWidget) and its panel
 * (HomeAssistantPanel): who's where, what needs a look, the thermostats, the
 * players, and every light and switch. Also which of them the owner lets
 * Cha Dash control (`attrs.control`) and the actions that do it.
 */
import type { GlyphKind } from '../components/marks/Glyph.vue';
import { plural, since } from '../lib/format';
import { attr } from '../model/graph';
import type { Resource } from '../model/types';

export const str = (v: unknown) => (typeof v === 'string' ? v : '');
export const num = (v: unknown) => (typeof v === 'number' && Number.isFinite(v) ? v : undefined);
/** "armed_home" → "Armed home". */
export const words = (v: string) => (v ? v[0]!.toUpperCase() + v.slice(1).replace(/_/g, ' ') : '');

/** An attr that holds a list of objects, else []. */
export function list(app: Resource | undefined, key: string): Record<string, unknown>[] {
  const v = attr<unknown>(app, key);
  return Array.isArray(v) ? v.filter((x): x is Record<string, unknown> => typeof x === 'object' && x !== null) : [];
}

const OPEN_WORDS: Record<string, string> = {
  garage: 'garage open',
  garage_door: 'garage open',
  window: 'window open',
  gate: 'gate open',
};
export const SAFETY_WORDS: Record<string, string> = {
  smoke: 'smoke',
  carbon_monoxide: 'carbon monoxide',
  gas: 'gas',
  moisture: 'water leak',
};

export interface Look {
  key: string;
  glyph: GlyphKind;
  name: string;
  detail: string;
}
/** What needs a look, most pressing first: tripped, open, unlocked, low batteries, updates. */
export function lookOf(app: Resource | undefined, unlocked: number): Look[] {
  const out: Look[] = [];
  for (const o of list(app, 'safety_items'))
    out.push({
      key: `s:${str(o['name'])}`,
      glyph: 'attn',
      name: str(o['name']),
      detail: SAFETY_WORDS[str(o['kind'])] ?? 'tripped',
    });
  for (const o of list(app, 'open_items'))
    out.push({
      key: `o:${str(o['name'])}`,
      glyph: 'attn',
      name: str(o['name']),
      detail: OPEN_WORDS[str(o['kind'])] ?? 'open',
    });
  if (unlocked)
    out.push({
      key: 'locks',
      glyph: 'lock',
      name: plural(unlocked, 'lock'),
      detail: 'unlocked',
    });
  for (const b of list(app, 'battery_items')) {
    const lvl = b['level'];
    out.push({
      key: `b:${str(b['name'])}`,
      glyph: 'attn',
      name: str(b['name']),
      detail: typeof lvl === 'number' ? `battery ${Math.round(lvl)}%` : 'battery low',
    });
  }
  for (const u of list(app, 'update_items')) {
    const from = str(u['installed']);
    const to = str(u['latest']);
    out.push({
      key: `u:${str(u['name'])}`,
      glyph: 'maint',
      name: str(u['name']),
      detail: from && to ? `${from} → ${to}` : 'update available',
    });
  }
  return out.filter((x) => x.name);
}
export const GLYPH_LABEL: Partial<Record<GlyphKind, string>> = {
  maint: 'update',
  lock: 'unlocked',
};

export interface Person {
  name: string;
  initial: string;
  home: boolean;
  /** "Home", "Away", or the zone's name. */
  where: string;
  since: string;
}
/** Every person, home first, then by name, so the marks read the same way every time. */
export function personsOf(app: Resource | undefined, now: number): Person[] {
  return list(app, 'people_items')
    .map((p) => {
      const name = str(p['name']);
      const where = str(p['where']);
      return {
        name,
        initial: (name.match(/\p{L}|\d/u)?.[0] ?? '?').toUpperCase(),
        home: where === 'home',
        where: where === 'unknown' || !where ? 'Unknown' : words(where),
        since: since(str(p['since']) || undefined, now),
      };
    })
    .filter((p) => p.name)
    .sort((a, b) => Number(b.home) - Number(a.home) || a.name.localeCompare(b.name));
}

const ACTIONS: Record<string, string> = {
  heating: 'heating',
  cooling: 'cooling',
  drying: 'drying',
  fan: 'fan on',
  idle: 'idle',
  off: 'off',
  heat: 'heat',
  cool: 'cool',
  heat_cool: 'auto',
  auto: 'auto',
  dry: 'dry',
  fan_only: 'fan',
};
/** Doing something worth saying on a facet (a mode alone isn't). */
export const ACTIVE = new Set(['heating', 'cooling', 'drying', 'fan on']);
export const deg = (v: number) => `${v.toFixed(1)}°`;

export interface Room {
  /** The climate entity; '' from an older hub or the Demo Home. */
  entity: string;
  name: string;
  current: number;
  target?: number | undefined;
  humidity?: number | undefined;
  action: string;
  /** The HVAC mode ("heat", "off"); '' when not sent. */
  mode: string;
  min?: number | undefined;
  max?: number | undefined;
  step?: number | undefined;
}
export function roomsOf(app: Resource | undefined): Room[] {
  return list(app, 'climate_items').flatMap((r) => {
    const current = num(r['current']);
    if (current === undefined) return [];
    return [
      {
        entity: str(r['entity']),
        name: str(r['name']) || 'Thermostat',
        current,
        target: num(r['target']),
        humidity: num(r['humidity']),
        action: ACTIONS[str(r['action'])] ?? '',
        mode: str(r['mode']),
        min: num(r['min']),
        max: num(r['max']),
        step: num(r['step']),
      },
    ];
  });
}
export const roomLine = (r: Room) =>
  [
    r.action && r.target !== undefined && r.action !== 'idle' && r.action !== 'off' ? `${r.action} to ${deg(r.target)}` : r.action || (r.target !== undefined ? `set ${deg(r.target)}` : ''),
    r.action === 'idle' && r.target !== undefined ? `set ${deg(r.target)}` : '',
    r.humidity !== undefined ? `${Math.round(r.humidity)}% humidity` : '',
  ]
    .filter(Boolean)
    .join(' · ');

export interface Track {
  key: string;
  player: string;
  title: string;
  artist: string;
}
/** What's playing now. */
export function playingOf(app: Resource | undefined): Track[] {
  return list(app, 'media_items')
    .map((p, i) => ({
      key: `${i}:${str(p['name'])}`,
      player: str(p['name']),
      title: str(p['title']),
      artist: str(p['artist']),
    }))
    .filter((p) => p.player || p.title);
}

/** A light, switch or media player as the panel lists it. */
export interface Device {
  entity: string;
  name: string;
  /** Home Assistant's state: on, off, playing, paused, idle, unavailable… */
  state: string;
  /** A light's brightness, 0–100, while on. */
  brightness?: number | undefined;
  title?: string | undefined;
  artist?: string | undefined;
}
/** `light_items`, `switch_items` or `player_items`: on (or playing) first, then by name, as the hub sends them. */
export function devicesOf(app: Resource | undefined, key: 'light_items' | 'switch_items' | 'player_items'): Device[] {
  return list(app, key)
    .map((d) => ({
      entity: str(d['entity']),
      name: str(d['name']) || str(d['entity']),
      state: str(d['state']),
      brightness: num(d['brightness']),
      title: str(d['title']) || undefined,
      artist: str(d['artist']) || undefined,
    }))
    .filter((d) => d.entity);
}

/** The domains the owner lets Cha Dash control on this instance (`control` in its settings). */
export type ControlDomain = 'light' | 'switch' | 'media_player' | 'climate';
export function controlOf(app: Resource | undefined): Set<ControlDomain> {
  const v = attr<unknown>(app, 'control');
  return new Set(Array.isArray(v) ? (v.filter((x) => typeof x === 'string') as ControlDomain[]) : []);
}
/** The hub lets Household switch lights only while switches aren't allowed (a switch can be a server's plug), and media and thermostats always. */
export function householdMay(control: Set<ControlDomain>, domain: ControlDomain): boolean {
  return domain === 'media_player' || domain === 'climate' || (domain === 'light' && !control.has('switch'));
}

/** The actions (integrations/homeassistant/control.go). */
export const ACT = {
  on: 'homeassistant.entity.on',
  off: 'homeassistant.entity.off',
  play: 'homeassistant.media.play',
  pause: 'homeassistant.media.pause',
  climate: 'homeassistant.climate.set',
} as const;

/** The panel's tabs, in order. */
export type PanelTab = 'people' | 'look' | 'lights' | 'switches' | 'climate' | 'media';
