/**
 * Which integration a discovered container is, so label discovery can
 * suggest that integration's catalog widget rather than the generic service
 * tile. Data only, and small on purpose: a container is matched by its
 * `homepage.widget.type` label first (Homepage's own word for it, mapped the
 * way the Homepage importer maps it, internal/importer/homepage/widgets.go),
 * else by its image's name (the last path segment, without the registry,
 * tag or digest: `lscr.io/linuxserver/sonarr:latest` is `sonarr`).
 *
 * `tile` is the catalog widget the integration's app feeds (model/widgets.ts);
 * integrations without a dedicated one get the service tile bound to their
 * app, which shows the per-integration stat (widgets/serviceStats.ts).
 */
import { attr } from './graph';
import type { Resource } from './types';

export interface KnownIntegration {
  /** The integration ID, which is also its apps' `attrs.type`. */
  id: string;
  name: string;
  /** The catalog widget its app feeds; `service` when there's no dedicated one. */
  tile: string;
  /** Image names (last path segment, lower case). */
  images: readonly string[];
  /** Homepage widget types that mean it, besides its ID. */
  homepage?: readonly string[];
}

const k = (id: string, name: string, tile: string, images: string[], homepage?: string[]): KnownIntegration =>
  homepage ? { id, name, tile, images, homepage } : { id, name, tile, images };

export const KNOWN_INTEGRATIONS: readonly KnownIntegration[] = [
  k('sonarr', 'Sonarr', 'arr', ['sonarr']),
  k('radarr', 'Radarr', 'arr', ['radarr']),
  k('lidarr', 'Lidarr', 'arr', ['lidarr']),
  k('plex', 'Plex', 'plex', ['plex', 'pms-docker']),
  k('jellyfin', 'Jellyfin', 'plex', ['jellyfin']),
  k('tautulli', 'Tautulli', 'service', ['tautulli']),
  k('homeassistant', 'Home Assistant', 'homeassistant', ['home-assistant', 'homeassistant']),
  k('frigate', 'Frigate', 'cameras', ['frigate']),
  k('uptimekuma', 'Uptime Kuma', 'uptimekuma', ['uptime-kuma']),
  k('traefik', 'Traefik', 'traefik', ['traefik']),
  k('qbittorrent', 'qBittorrent', 'downloads', ['qbittorrent', 'qbittorrent-nox']),
  k('transmission', 'Transmission', 'downloads', ['transmission']),
  k('sabnzbd', 'SABnzbd', 'downloads', ['sabnzbd']),
  k('nzbget', 'NZBGet', 'downloads', ['nzbget']),
  k('prowlarr', 'Prowlarr', 'service', ['prowlarr']),
  k('bazarr', 'Bazarr', 'service', ['bazarr']),
  k('seerr', 'Seerr', 'service', ['seerr', 'overseerr', 'jellyseerr'], ['overseerr', 'jellyseerr']),
  k('immich', 'Immich', 'service', ['immich-server']),
  k('mealie', 'Mealie', 'service', ['mealie', 'mealie-nightly']),
  k('linkding', 'Linkding', 'service', ['linkding']),
  k('karakeep', 'Karakeep', 'service', ['karakeep', 'hoarder'], ['hoarder']),
  k('paperlessngx', 'Paperless-ngx', 'service', ['paperless-ngx']),
  k('audiobookshelf', 'Audiobookshelf', 'service', ['audiobookshelf']),
  k('nextcloud', 'Nextcloud', 'service', ['nextcloud']),
  k('speedtesttracker', 'Speedtest Tracker', 'service', ['speedtest-tracker'], ['speedtest']),
  k('pihole', 'Pi-hole', 'service', ['pihole']),
  k('adguard', 'AdGuard Home', 'service', ['adguardhome']),
];

/** The image's name: `ghcr.io/hotio/sonarr:release@sha256:…` → `sonarr`. */
export function imageName(image: string): string {
  const noDigest = image.split('@')[0] ?? '';
  const last = noDigest.split('/').pop() ?? '';
  return (last.split(':')[0] ?? '').trim().toLowerCase();
}

/** The integration a container is, by its Homepage widget label, else its image. */
export function knownIntegrationOf(r: Resource): KnownIntegration | undefined {
  const widget = (r.labels?.['homepage.widget.type'] ?? '').trim().toLowerCase();
  if (widget) {
    const hit = KNOWN_INTEGRATIONS.find((x) => x.id === widget || x.homepage?.includes(widget));
    if (hit) return hit;
  }
  const image = attr<string>(r, 'image');
  if (typeof image !== 'string' || !image) return undefined;
  const name = imageName(image);
  return name ? KNOWN_INTEGRATIONS.find((x) => x.images.includes(name)) : undefined;
}
