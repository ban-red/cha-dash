/**
 * Bundled service icons: web/public/service-icons/<name>.svg, from the
 * Apache-2.0 dashboard-icons project (see LICENSE-dashboard-icons.txt there).
 * The logos remain trademarks of their owners. A manifest's `icon` slug
 * (integration.Manifest.Icon) names one; ours differ from the file names in a
 * few places, which ALIASES maps. Unknown slugs return undefined and the
 * caller keeps its monogram.
 */
export const SERVICE_ICONS: ReadonlySet<string> = new Set([
  'actual-budget',
  'adguard-home',
  'audiobookshelf',
  'authelia',
  'authentik',
  'bazarr',
  'bitwarden',
  'bookstack',
  'caddy',
  'calibre-web',
  'changedetection',
  'cloudflare',
  'docker',
  'dozzle',
  'duplicati',
  'emby',
  'esphome',
  'filebrowser',
  'firefly-iii',
  'flaresolverr',
  'forgejo',
  'freshrss',
  'frigate',
  'gitea',
  'github',
  'gitlab',
  'gotify',
  'grafana',
  'grocy',
  'healthchecks',
  'homarr',
  'home-assistant',
  'immich',
  'influxdb',
  'jellyfin',
  'jellyseerr',
  'joplin',
  'karakeep',
  'kavita',
  'keycloak',
  'komga',
  'lidarr',
  'linkding',
  'loki',
  'mariadb',
  'mealie',
  'miniflux',
  'minio',
  'mongodb',
  'mosquitto',
  'n8n',
  'navidrome',
  'netdata',
  'nextcloud',
  'nginx',
  'nginx-proxy-manager',
  'node-red',
  'ntfy',
  'nzbget',
  'ollama',
  'open-webui',
  'opnsense',
  'outline',
  'overseerr',
  'paperless-ngx',
  'photoprism',
  'pi-hole',
  'plex',
  'portainer',
  'postgresql',
  'prometheus',
  'prowlarr',
  'proxmox',
  'proxmox-backup-server',
  'qbittorrent',
  'radarr',
  'readarr',
  'redis',
  'sabnzbd',
  'searxng',
  'seerr',
  'sonarr',
  'stirling-pdf',
  'syncthing',
  'synology',
  'tailscale',
  'tautulli',
  'tdarr',
  'traefik',
  'transmission',
  'trilium',
  'truenas',
  'unraid',
  'uptime-kuma',
  'vaultwarden',
  'vikunja',
  'wallabag',
  'watchtower',
  'whisparr',
  'wikijs',
  'wireguard',
  'zigbee2mqtt',
]);

const ALIASES: Record<string, string> = {
  homeassistant: 'home-assistant',
  uptimekuma: 'uptime-kuma',
  paperlessngx: 'paperless-ngx',
};

const BASE = '/service-icons/';

/** The same-origin URL of the bundled icon for a manifest slug, if there is one. */
export function serviceIconUrl(slug: string | undefined): string | undefined {
  if (!slug) return undefined;
  const key = slug.toLowerCase();
  const name = SERVICE_ICONS.has(key) ? key : ALIASES[key];
  return name && SERVICE_ICONS.has(name) ? `${BASE}${name}.svg` : undefined;
}

const squash = (s: string) => s.toLowerCase().replace(/[^a-z0-9]+/g, '');

let byKey: Map<string, string> | undefined;
/** squashed name → file name: "pihole" and "pi-hole" both land on pi-hole. Built on first use. */
function keys(): Map<string, string> {
  if (!byKey) {
    byKey = new Map();
    for (const n of SERVICE_ICONS) byKey.set(squash(n), n);
    for (const [a, n] of Object.entries(ALIASES)) byKey.set(squash(a), n);
  }
  return byKey;
}

const memo = new Map<string, string | undefined>();

/**
 * A bundled icon found by a display name or any string: "Sonarr", "Plex Media
 * Server", "uptime-kuma (prod)". Tries the whole string, then each word, then
 * each pair of adjacent words, so "Uptime Kuma" and "Home Assistant" match
 * while "Media Server" doesn't. Bracketed qualifiers are ignored. Maps and a memo make repeat calls O(1).
 */
export function serviceIconByName(text: string | undefined): string | undefined {
  if (!text) return undefined;
  if (memo.has(text)) return memo.get(text);
  const k = keys();
  // A qualifier in brackets ("Pluto PVE (main opnsense)") describes the service, it isn't one.
  const main = text.replace(/\([^)]*\)|\[[^\]]*\]/g, ' ').trim() || text;
  let hit = k.get(squash(main));
  if (!hit) {
    const w = main.toLowerCase().split(/[^a-z0-9]+/).filter(Boolean);
    for (let i = 0; !hit && i < w.length; i++) {
      if (i + 1 < w.length) hit = k.get(w[i]! + w[i + 1]!);
      hit ??= k.get(w[i]!);
    }
  }
  const url = hit ? `${BASE}${hit}.svg` : undefined;
  if (memo.size > 500) memo.clear();
  memo.set(text, url);
  return url;
}
