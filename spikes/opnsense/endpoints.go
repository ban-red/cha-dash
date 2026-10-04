package main

// Endpoint is one read-only API call in the catalogue.
type Endpoint struct {
	Path   string
	Method string // GET (default) or HEAD
	Priv   string // OPNsense privilege (ACL display name, 26.7) that grants it
	Note   string
}

// catalogue lists only GET endpoints whose controller action has no isPost()
// branch that changes anything (checked against opnsense/core 26.7.5). Paths
// use the snake_case form the 26.x ACL patterns match.
//
// Deliberately absent (see README): every POST-only action (firmware check,
// service start/stop, reconfigure, dismiss_status, query_states,
// query_pf_top, flush/kill/del*), the config backup download, the firmware
// config (may hold a subscription key), and anything listing users.
var catalogue = []Endpoint{
	// Identity, version, health
	{Path: "/api/core/firmware/status", Priv: "System: Firmware", Note: "GET only reads the cached product/check result; POST would run a probe"},
	{Path: "/api/core/firmware/running", Priv: "System: Firmware"},
	{Path: "/api/core/system/status", Priv: "System: Status", Note: "system status messages (the bell icon)"},
	{Path: "/api/diagnostics/system/system_information", Priv: "Lobby: Dashboard", Note: "name, versions, updates"},
	{Path: "/api/diagnostics/system/system_resources", Priv: "Lobby: Dashboard", Note: "memory"},
	{Path: "/api/diagnostics/system/system_time", Priv: "Lobby: Dashboard", Note: "uptime, boottime, loadavg"},
	{Path: "/api/diagnostics/system/system_temperature", Priv: "Lobby: Dashboard"},
	{Path: "/api/diagnostics/system/system_disk", Priv: "Lobby: Dashboard"},
	{Path: "/api/diagnostics/system/system_swap", Priv: "Lobby: Dashboard"},
	{Path: "/api/diagnostics/system/system_mbuf", Priv: "Lobby: Dashboard"},
	{Path: "/api/diagnostics/cpu_usage/get_cpu_type", Priv: "Lobby: Dashboard"},
	{Path: "/api/diagnostics/firewall/pf_states", Priv: "Lobby: Dashboard", Note: "state table size: current and limit only"},
	{Path: "/api/diagnostics/activity/get_activity", Priv: "Diagnostics: System Activity", Note: "top(1): CPU/mem headers plus the full process list"},
	{Path: "/api/core/menu/tree", Priv: "(none listed)", Note: "menu filtered by the caller's ACL: a privilege fingerprint"},

	// Interfaces and traffic
	{Path: "/api/diagnostics/traffic/interface", Priv: "Reporting: Traffic", Note: "byte/packet counters per interface"},
	{Path: "/api/interfaces/overview/interfaces_info", Priv: "Status: Interfaces"},
	{Path: "/api/diagnostics/interface/get_interface_names", Priv: "Diagnostics: Logs: Firewall: Live View"},
	{Path: "/api/diagnostics/interface/get_interface_statistics", Priv: "Diagnostics: Netstat"},
	{Path: "/api/diagnostics/systemhealth/get_rrd_list", Priv: "Diagnostics: System Health", Note: "RRD history: could backfill the sparkline"},

	// Gateways
	{Path: "/api/routes/gateway/status", Priv: "System: Gateways", Note: "dpinger: loss, delay, stddev, status"},

	// Clients: counts only, never names
	{Path: "/api/diagnostics/interface/get_arp", Priv: "Diagnostics: ARP Table"},
	{Path: "/api/kea/leases4/search", Priv: "Services: DHCP: Kea(v4)", Note: "no isPost check; GET returns the first page"},
	{Path: "/api/dnsmasq/leases/search", Priv: "Services: Dnsmasq DNS/DHCP: Settings", Note: "no isPost check"},

	// Firewall and services
	{Path: "/api/diagnostics/firewall/pf_statistics/info", Priv: "Diagnostics: Firewall statistics"},
	{Path: "/api/diagnostics/firewall/list_rule_ids", Priv: "Diagnostics: Firewall sessions"},
	{Path: "/api/core/service/search", Priv: "Status: Services", Note: "no isPost check; the same privilege also grants start/stop/restart"},
}
