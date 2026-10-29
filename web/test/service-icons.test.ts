import { existsSync, readdirSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import { SERVICE_ICONS, serviceIconUrl } from '../src/lib/serviceIcons';

const dir = resolve(__dirname, '../public/service-icons');

describe('service icons', () => {
  it('lists exactly the bundled files', () => {
    const files = readdirSync(dir).filter((f) => f.endsWith('.svg')).map((f) => f.slice(0, -4)).sort();
    expect([...SERVICE_ICONS].sort()).toEqual(files);
  });

  it('resolves manifest slugs, including aliases', () => {
    for (const slug of ['sonarr', 'proxmox', 'proxmox-backup-server', 'homeassistant', 'uptimekuma', 'paperlessngx']) {
      const url = serviceIconUrl(slug);
      expect(url, slug).toBeDefined();
      expect(existsSync(resolve(dir, url!.replace('/service-icons/', ''))), slug).toBe(true);
    }
  });

  it('returns nothing for unknown or empty slugs', () => {
    expect(serviceIconUrl('nope')).toBeUndefined();
    expect(serviceIconUrl(undefined)).toBeUndefined();
    expect(serviceIconUrl('')).toBeUndefined();
  });
});

describe('tile fallback', () => {
  it('uses the integration logo for a bound tile without a cached icon', async () => {
    const { iconFor } = await import('../src/stores/icons');
    const { integrations } = await import('../src/stores/integrations');
    integrations.instances = [{ id: 'in_1', integration: 'sonarr', name: 'Sonarr', status: 'ok' }];
    const empty = { by_icon: {}, by_origin: {}, by_instance: {} };
    expect(iconFor(empty, { bind: { instance: 'in_1' } } as never, undefined)).toBe('/service-icons/sonarr.svg');
    expect(iconFor(empty, { bind: { instance: 'in_x' } } as never, undefined)).toBeUndefined();
    const cached = { ...empty, by_instance: { in_1: '/api/icons/ic_abcdefgh12' } };
    expect(iconFor(cached, { bind: { instance: 'in_1' } } as never, undefined)).toBe('/api/icons/ic_abcdefgh12');
    // a tile named for a service keeps its own logo on a host's instance
    integrations.instances = [{ id: 'in_u', integration: 'unraid', name: 'Unraid', status: 'ok' }];
    const onUnraid = { by_icon: {}, by_origin: {}, by_instance: { in_u: '/api/icons/ic_unraidfav1' } };
    expect(iconFor(onUnraid, { bind: { instance: 'in_u' } } as never, undefined, 'Sonarr')).toBe('/service-icons/sonarr.svg');
    expect(iconFor(onUnraid, { bind: { instance: 'in_u' } } as never, undefined, 'Tower')).toBe('/api/icons/ic_unraidfav1');
    integrations.instances = [];
  });
});

describe('lookup by name', () => {
  it('finds a logo from a display name', async () => {
    const { serviceIconByName } = await import('../src/lib/serviceIcons');
    const f = (s?: string) => serviceIconByName(s);
    expect(f('Sonarr')).toBe('/service-icons/sonarr.svg');
    expect(f('Plex Media Server')).toBe('/service-icons/plex.svg');
    expect(f('Uptime Kuma')).toBe('/service-icons/uptime-kuma.svg');
    expect(f('pihole (kitchen)')).toBe('/service-icons/pi-hole.svg');
    expect(f('Home Assistant')).toBe('/service-icons/home-assistant.svg');
    expect(f('Media Server')).toBeUndefined();
    expect(f('Pluto PVE (main opnsense)')).toBeUndefined();
    expect(f('')).toBeUndefined();
    expect(f(undefined)).toBeUndefined();
    expect(f('Sonarr')).toBe(f('Sonarr'));
  });
});
