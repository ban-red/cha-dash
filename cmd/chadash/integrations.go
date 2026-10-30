package main

// The integrations this hub includes. Each registers itself at init (see
// internal/integration); add a line here for every new one.
import (
	_ "dash.cha.sh/integrations/adguard"        // AdGuard Home: queries, blocked, blocklist
	_ "dash.cha.sh/integrations/audiobookshelf" // Audiobookshelf: books, podcasts, sessions
	_ "dash.cha.sh/integrations/checks"         // built-in HTTP and TCP checks
	_ "dash.cha.sh/integrations/docker"         // Docker engines through a socket proxy
	_ "dash.cha.sh/integrations/frigate"        // Frigate NVR: cameras, detections
	_ "dash.cha.sh/integrations/homeassistant"  // Home Assistant: who's home, lights, power
	_ "dash.cha.sh/integrations/ical"           // calendar feeds (.ics): Google, iCloud, Outlook
	_ "dash.cha.sh/integrations/immich"         // Immich photo library statistics
	_ "dash.cha.sh/integrations/jellyfin"       // Jellyfin: streams, libraries
	_ "dash.cha.sh/integrations/lidarr"         // Lidarr: queue, wanted, calendar
	_ "dash.cha.sh/integrations/nextcloud"      // Nextcloud: users, files, free space
	_ "dash.cha.sh/integrations/nzbget"         // NZBGet: speed, queue
	_ "dash.cha.sh/integrations/opnsense"       // OPNsense WAN traffic (counters + SSE)
	_ "dash.cha.sh/integrations/pbs"            // Proxmox Backup Server
	_ "dash.cha.sh/integrations/pihole"         // Pi-hole v6: queries, blocked, blocklist
	_ "dash.cha.sh/integrations/plex"           // Plex: streams, libraries, PIN sign-in
	_ "dash.cha.sh/integrations/prowlarr"       // Prowlarr: indexers, grabs
	_ "dash.cha.sh/integrations/proxmox"        // Proxmox VE
	_ "dash.cha.sh/integrations/qbittorrent"    // qBittorrent: speeds, torrents
	_ "dash.cha.sh/integrations/radarr"         // Radarr: queue, wanted, calendar
	_ "dash.cha.sh/integrations/sonarr"         // Sonarr: queue, wanted, calendar
	_ "dash.cha.sh/integrations/traefik"        // Traefik routers, services and backends
	_ "dash.cha.sh/integrations/transmission"   // Transmission: speeds, torrents
	_ "dash.cha.sh/integrations/unraid"         // Unraid API (GraphQL)
	_ "dash.cha.sh/integrations/uptimekuma"     // Uptime Kuma: a status page's monitors
	_ "dash.cha.sh/integrations/weather"        // weather from Open-Meteo (keyless)
	_ "dash.cha.sh/specs"                       // the bundled Tier 1 specs
)
