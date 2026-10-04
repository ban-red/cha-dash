/**
 * The dogfood front page: the shape web/src/model/defaultLayout.ts builds
 * from a full Home Graph, with the sizes web/src/model/widgets.ts declares.
 *
 *   Home (4)            │ Infrastructure (8)
 *   cameras    large    │ fleet large · OPNsense wide · backups · containers
 *   Home Asst. wide     │ host wide · Traefik · Uptime Kuma
 *   ────────────────────┼─────────────────────────────
 *   Media (8)                                │ Activity (4)
 *   Now playing wide · Sonarr · Radarr       │ incidents large
 *   downloads wide · up next wide            │
 *
 * 15 widgets in 4 sections. Every section is packed solid at 12 columns, so
 * the board has no free cell: "insert into a full board" uses it as is.
 *
 * One change from the web catalog: Now playing also declares Large, so the
 * "Wide → Large next to a full row" scenario has a Wide widget that can grow.
 */
import type { BoardData, WidgetData, SizeName } from './types.ts';

const w = (id: string, type: string, size: SizeName, supportedSizes: SizeName[]): WidgetData => ({ id, type, size, supportedSizes });

export const DOGFOOD: BoardData = {
  sections: [
    {
      id: 'home',
      title: 'Home',
      span: 4,
      items: [w('cameras', 'cameras', 'large', ['wide', 'large', 'hero']), w('homeassistant', 'homeassistant', 'wide', ['small', 'wide'])],
    },
    {
      id: 'infra',
      title: 'Infrastructure',
      span: 8,
      items: [
        w('fleet', 'fleet', 'large', ['wide', 'large', 'hero']),
        w('opnsense', 'opnsense', 'wide', ['small', 'wide']),
        w('backups', 'backups', 'small', ['small', 'wide']),
        w('containers', 'containers', 'small', ['small', 'wide', 'large']),
        w('host', 'host', 'wide', ['small', 'wide']),
        w('traefik', 'traefik', 'small', ['small', 'wide']),
        w('uptimekuma', 'uptimekuma', 'small', ['small', 'wide']),
      ],
    },
    {
      id: 'media',
      title: 'Media',
      span: 8,
      items: [
        w('plex', 'plex', 'wide', ['small', 'wide', 'large']),
        w('sonarr', 'arr', 'small', ['small', 'wide']),
        w('radarr', 'arr', 'small', ['small', 'wide']),
        w('downloads', 'downloads', 'wide', ['small', 'wide']),
        w('calendar', 'calendar', 'wide', ['wide', 'tall']),
      ],
    },
    {
      id: 'activity',
      title: 'Activity',
      span: 4,
      items: [w('incidents', 'incidents', 'large', ['wide', 'tall', 'large'])],
    },
  ],
};

/** A widget the front page doesn't have yet, for the insert scenarios. */
export const PIHOLE: WidgetData = w('pihole', 'pihole', 'small', ['small', 'wide']);

/**
 * A 60-widget board for the timings: the dogfood page's four sections, each
 * repeated four times over (15 widgets per section), ids suffixed.
 */
export function sixtyWidgetBoard(): BoardData {
  const all = DOGFOOD.sections.flatMap((s) => s.items);
  return {
    sections: DOGFOOD.sections.map((s, si) => ({
      ...s,
      items: Array.from({ length: 15 }, (_, i) => {
        const src = all[(si * 15 + i) % all.length]!;
        // keep every widget inside its section: a 4-column section can't hold a hero
        const fits: SizeName[] = src.supportedSizes.filter((z) => z !== 'hero' && z !== 'strip-long');
        const size = fits.includes(src.size) ? src.size : fits[0]!;
        return { ...src, id: `${src.id}-${si}-${i}`, size, supportedSizes: fits };
      }),
    })),
  };
}
