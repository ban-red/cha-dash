package homepage

// field maps a Homepage widget field onto an instance config key.
type field struct {
	from, to string
	// secret fields go to the vault; config holds a ${secret:…} reference.
	secret bool
	// optional fields may be absent without the instance needing repair.
	optional bool
}

// widgetSpec maps one Homepage widget type onto a Cha Dash integration
// (docs/plan/07-v0.1.md → Integration IDs and Instance config keys) and a
// tile.
type widgetSpec struct {
	integration string
	// tile is the widget type from web/src/model/widgets.ts, or "service"
	// (the generic service tile) when the catalog has no dedicated widget.
	tile string
	// bound tiles target the app resource: "<tile>@app:<integration>".
	bound  bool
	fields []field
	// options maps Homepage widget fields to tile display options.
	options map[string]string
}

// Identifiers (a Proxmox token ID, a username) are written to config, not
// the vault: they aren't secret, and vaulting them would make every
// "api@pam!homepage" look like a reused secret. The 18 values the dogfood
// fixture counts are exactly the passwords, keys and tokens.
var (
	apiKey  = []field{{from: "key", to: "api_key", secret: true}}
	token   = []field{{from: "key", to: "token", secret: true}}
	pveAuth = []field{{from: "username", to: "token_id"}, {from: "password", to: "token_secret", secret: true}}
	basic   = []field{{from: "username", to: "username", optional: true}, {from: "password", to: "password", secret: true, optional: true}}
)

var widgetSpecs = map[string]widgetSpec{
	"homeassistant": {integration: "homeassistant", tile: "homeassistant", fields: token,
		options: map[string]string{"custom": "custom"}},
	"frigate": {integration: "frigate", tile: "cameras", fields: basic,
		options: map[string]string{"enableRecentEvents": "recent_events"}},
	"opnsense": {integration: "opnsense", tile: "opnsense",
		fields:  []field{{from: "username", to: "key", secret: true}, {from: "password", to: "secret", secret: true}},
		options: map[string]string{"wan": "wan"}},
	"proxmox": {integration: "proxmox", tile: "service", fields: pveAuth,
		options: map[string]string{"node": "node"}},
	"proxmoxbackupserver": {integration: "pbs", tile: "service", fields: pveAuth,
		options: map[string]string{"datastore": "datastore"}},
	"traefik": {integration: "traefik", tile: "traefik", fields: basic},
	"uptimekuma": {integration: "uptimekuma", tile: "uptimekuma",
		fields: []field{{from: "slug", to: "slug"}, {from: "key", to: "key", secret: true, optional: true}}},
	"unraid": {integration: "unraid", tile: "service", fields: apiKey,
		options: map[string]string{"pool1": "pool1", "pool2": "pool2", "pool3": "pool3", "pool4": "pool4"}},
	"plex": {integration: "plex", tile: "plex", fields: token},
	"sonarr": {integration: "sonarr", tile: "arr", bound: true, fields: apiKey,
		options: map[string]string{"enableQueue": "queue"}},
	"radarr": {integration: "radarr", tile: "arr", bound: true, fields: apiKey,
		options: map[string]string{"enableQueue": "queue"}},
	"prowlarr": {integration: "prowlarr", tile: "service", fields: apiKey},
	"bazarr":   {integration: "bazarr", tile: "service", fields: apiKey},
	"tautulli": {integration: "tautulli", tile: "service", fields: apiKey,
		options: map[string]string{"enableUser": "show_user", "showEpisodeNumber": "show_episode_number", "expandOneStreamToTwoRows": "expand_single_stream"}},
	"qbittorrent": {integration: "qbittorrent", tile: "downloads",
		fields:  []field{{from: "username", to: "username"}, {from: "password", to: "password", secret: true}},
		options: map[string]string{"enableLeechProgress": "leech_progress"}},
	"immich":   {integration: "immich", tile: "service", fields: apiKey},
	"mealie":   {integration: "mealie", tile: "service", fields: token},
	"karakeep": {integration: "karakeep", tile: "service", fields: apiKey},
	"hoarder":  {integration: "karakeep", tile: "service", fields: apiKey}, // Karakeep's old name
	// Seerr is the merged successor of both; the spec reads either.
	"overseerr":  {integration: "seerr", tile: "service", fields: apiKey},
	"jellyseerr": {integration: "seerr", tile: "service", fields: apiKey},
	// v0.2's breadth integrations.
	"lidarr": {integration: "lidarr", tile: "arr", bound: true, fields: apiKey,
		options: map[string]string{"enableQueue": "queue"}},
	"transmission": {integration: "transmission", tile: "downloads",
		fields: []field{{from: "username", to: "username", optional: true}, {from: "password", to: "password", secret: true, optional: true},
			{from: "rpcUrl", to: "rpc_path", optional: true}}},
	"nzbget": {integration: "nzbget", tile: "downloads", fields: basic},
	// Nextcloud takes a serverinfo token (Homepage's key) or a username and
	// app password; either is enough, so neither is required.
	"nextcloud": {integration: "nextcloud", tile: "service",
		fields: []field{{from: "key", to: "token", secret: true, optional: true}, {from: "username", to: "username", optional: true},
			{from: "password", to: "password", secret: true, optional: true}}},
	"audiobookshelf": {integration: "audiobookshelf", tile: "service", fields: apiKey},
	// Paperless-ngx reads with a token only: Homepage's username and
	// password are listed as ignored, and the instance asks for a token.
	"paperlessngx": {integration: "paperlessngx", tile: "service", fields: token},
	"speedtest":    {integration: "speedtesttracker", tile: "service", fields: token},
	// Homepage has no widget for these, but Cha Dash has an integration, so a
	// service recognised as one (see detectApp) is set up with it.
	"linkding": {integration: "linkding", tile: "service", fields: token},
	"sabnzbd":  {integration: "sabnzbd", tile: "downloads", fields: apiKey},
}

// appAliases are other names a service goes by: the normalised icon name or
// service name on the left, a widgetSpecs key on the right. Every widgetSpecs
// key is an alias of itself.
var appAliases = map[string]string{
	"homeassistant":    "homeassistant",
	"speedtesttracker": "speedtest",
	"pbs":              "proxmoxbackupserver",
	"proxmoxve":        "proxmox",
	"seerr":            "overseerr",
	"paperless":        "paperlessngx",
	"uptimekuma":       "uptimekuma",
	"audiobookshelf":   "audiobookshelf",
	"proxmoxbackup":    "proxmoxbackupserver",
}

// Fields every widget may carry. Version is Homepage's API-version switch;
// Cha Dash integrations negotiate the version themselves.
var commonWidgetFields = map[string]bool{"type": true, "url": true, "fields": true, "version": true}

// calendarCapability is what the media calendar reads from Sonarr, Radarr and
// Lidarr.
const calendarCapability = "media.calendar"

// calendarSources are the Homepage calendar integrations Cha Dash can read.
var calendarSources = map[string]bool{"sonarr": true, "radarr": true, "lidarr": true}

// Entry fields the importer understands. Anything else is listed as ignored.
var entryFields = map[string]bool{
	"href": true, "icon": true, "description": true, "name": true,
	"siteMonitor": true, "ping": true, "server": true, "container": true,
	"widget": true, "widgets": true, "weight": true, "showStats": true,
}

// entryIgnored gives the reason for entry fields Cha Dash deliberately drops.
var entryIgnored = map[string]string{
	"statusStyle": "status style is a theme setting in Cha Dash",
	"target":      "links open the way the browser decides",
	"app":         "Kubernetes discovery isn't supported",
	"namespace":   "Kubernetes discovery isn't supported",
	"podSelector": "Kubernetes discovery isn't supported",
}

// settingsIgnored lists settings.yaml keys that aren't imported, and why.
var settingsIgnored = map[string]string{
	"background":               "Cha Dash doesn't put content over a wallpaper",
	"cardBlur":                 "Cha Dash doesn't put content over a wallpaper",
	"color":                    "Cha Dash has its own themes (light, dark, OLED)",
	"theme":                    "Cha Dash has its own themes (light, dark, OLED)",
	"statusStyle":              "status display is part of the Cha Dash theme",
	"useEqualHeights":          "tile heights come from widget sizes",
	"headerStyle":              "section headers are part of the Cha Dash theme",
	"disableCollapse":          "Cha Dash sections don't collapse",
	"groupsInitiallyCollapsed": "Cha Dash sections don't collapse",
	"hideVersion":              "Cha Dash shows its version in settings",
	"providers":                "weather providers aren't imported (their API keys aren't moved either)",
	"fullWidth":                "boards always use the full grid",
	"maxGroupColumns":          "section widths come from the layout grid",
	"maxBookmarkGroupColumns":  "bookmarks aren't imported yet",
	"bookmarksStyle":           "bookmarks aren't imported yet",
	"favicon":                  "Cha Dash uses its own icon",
	"base":                     "Cha Dash's public URL is CHADASH_PUBLIC_URL",
	"target":                   "links open the way the browser decides",
	"language":                 "Cha Dash follows the browser's language",
	"logpath":                  "Cha Dash logs to stdout",
	"showStats":                "container stats show on demand",
	"hideErrors":               "integration errors go to the Inbox as repairs",
	"disableIndexing":          "Cha Dash is never indexed",
	"disableUpdateCheck":       "not applicable",
	"startUrl":                 "not applicable",
	"description":              "not applicable",
	"pwa":                      "Cha Dash is an installable PWA",
	"iconStyle":                "icons are part of the Cha Dash theme",
	"blockHighlights":          "not supported yet",
}

// quicklaunchKnown are quicklaunch keys Cha Dash honors by default (the
// value says how), plus searchDescriptions, which becomes a board setting.
var quicklaunchKnown = map[string]string{
	"searchDescriptions": "",
	"hideInternetSearch": "⌘K has no internet search",
	"hideVisitURL":       "⌘K doesn't offer to visit typed URLs",
}
