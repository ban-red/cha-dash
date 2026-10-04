import { SIZE_UNITS, type SizeName } from '../scenarios/index.ts';

export const NAMES: Record<string, string> = {
  cameras: 'Cameras',
  homeassistant: 'Home Assistant',
  fleet: 'Fleet',
  opnsense: 'OPNsense',
  backups: 'Backups',
  containers: 'Containers',
  host: 'Host',
  traefik: 'Traefik',
  uptimekuma: 'Uptime Kuma',
  plex: 'Now playing',
  sonarr: 'Sonarr',
  radarr: 'Radarr',
  downloads: 'Downloads',
  calendar: 'Up next',
  incidents: 'Incidents',
};

export const SIZE_NAMES: Record<SizeName, string> = {
  glyph: 'Glyph',
  strip: 'Strip',
  'strip-long': 'Strip',
  small: 'Small',
  wide: 'Wide',
  tall: 'Tall',
  large: 'Large',
  hero: 'Hero',
};

export function sizeOf(w: number, h: number): SizeName | undefined {
  return (Object.keys(SIZE_UNITS) as SizeName[]).find((s) => SIZE_UNITS[s].w === w && SIZE_UNITS[s].h === h);
}
