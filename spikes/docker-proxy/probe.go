package main

import (
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ProbeRow is one line of the proxy-vs-raw probe. Only status codes and the
// response's content type are recorded, never bodies.
type ProbeRow struct {
	Method      string `json:"method"`
	Path        string `json:"path"` // template: {api}, {id}, {image}, {network}, {volume}
	Group       string `json:"group"`
	Status      int    `json:"status"`
	ContentType string `json:"contentType,omitempty"`
	Error       string `json:"error,omitempty"`
	RawExpect   string `json:"rawExpect"` // what an unfiltered Docker API answers
}

// Probe groups.
const (
	gNeeded  = "needed"  // the hub's v0.1 read model uses it
	gExtra   = "extra"   // read-only, but the hub doesn't need it
	gBlocked = "blocked" // a filtering proxy should refuse it
)

type probeSpec struct {
	method, tmpl, group, rawExpect string
	path                           func(ids) string
}

type ids struct{ api, container, image, network, volume string }

var probes = []probeSpec{
	{"HEAD", "/_ping", gNeeded, "200", func(i ids) string { return "/_ping" }},
	{"GET", "/_ping", gNeeded, "200", func(i ids) string { return "/_ping" }},
	{"GET", "/version", gNeeded, "200", func(i ids) string { return "/version" }},
	{"GET", "/v{api}/version", gNeeded, "200", func(i ids) string { return i.api + "/version" }},
	{"GET", "/v{api}/info", gNeeded, "200", func(i ids) string { return i.api + "/info" }},
	{"GET", "/v{api}/events?since&until (past 60s)", gNeeded, "200", func(i ids) string {
		now := time.Now().Unix()
		return i.api + "/events?" + url.Values{"since": {strconv.FormatInt(now-60, 10)}, "until": {strconv.FormatInt(now, 10)}}.Encode()
	}},
	{"GET", "/v{api}/containers/json?all=1&limit=1", gNeeded, "200", func(i ids) string { return i.api + "/containers/json?all=1&limit=1" }},
	{"GET", "/v{api}/containers/{id}/json", gNeeded, "200", func(i ids) string { return i.api + "/containers/" + i.container + "/json" }},
	{"GET", "/v{api}/containers/{id}/stats?stream=false&one-shot=true", gNeeded, "200", func(i ids) string {
		return i.api + "/containers/" + i.container + "/stats?stream=false&one-shot=true"
	}},
	{"GET", "/v{api}/images/json", gNeeded, "200", func(i ids) string { return i.api + "/images/json" }},
	{"GET", "/v{api}/images/{image}/json", gNeeded, "200", func(i ids) string { return i.api + "/images/" + i.image + "/json" }},
	{"GET", "/v{api}/networks", gNeeded, "200", func(i ids) string { return i.api + "/networks" }},
	{"GET", "/v{api}/networks/{network}", gNeeded, "200", func(i ids) string { return i.api + "/networks/" + i.network }},
	{"GET", "/v{api}/volumes", gNeeded, "200", func(i ids) string { return i.api + "/volumes" }},
	{"GET", "/v{api}/volumes/{volume}", gNeeded, "200", func(i ids) string { return i.api + "/volumes/" + i.volume }},
	{"GET", "/v{api}/system/df?type=image", gExtra, "200", func(i ids) string { return i.api + "/system/df?type=image" }},
	{"GET", "/v{api}/plugins", gExtra, "200", func(i ids) string { return i.api + "/plugins" }},
	{"GET", "/v{api}/swarm", gBlocked, "503 (not a swarm manager)", func(i ids) string { return i.api + "/swarm" }},
	{"GET", "/v{api}/secrets", gBlocked, "503 (not a swarm manager)", func(i ids) string { return i.api + "/secrets" }},
	{"GET", "/v{api}/configs", gBlocked, "503 (not a swarm manager)", func(i ids) string { return i.api + "/configs" }},
	{"GET", "/v{api}/services", gBlocked, "503 (not a swarm manager)", func(i ids) string { return i.api + "/services" }},
	{"GET", "/v{api}/tasks", gBlocked, "503 (not a swarm manager)", func(i ids) string { return i.api + "/tasks" }},
	{"GET", "/v{api}/nodes", gBlocked, "503 (not a swarm manager)", func(i ids) string { return i.api + "/nodes" }},
	{"GET", "/v{api}/containers/{id}/logs?stdout=0&stderr=0&tail=0", gBlocked, "400 (no stream chosen; never returns logs)", func(i ids) string {
		return i.api + "/containers/" + i.container + "/logs?stdout=0&stderr=0&tail=0"
	}},
}

func (e *endpoint) probe(list []listEntry, volume string, imgBody, netBody, volBody []byte) []ProbeRow {
	var i ids
	i.api = e.prefix
	for _, c := range list {
		if c.State == "running" {
			i.container, i.image = c.ID, c.ImageID
			break
		}
	}
	if i.container == "" && len(list) > 0 {
		i.container, i.image = list[0].ID, list[0].ImageID
	}
	// Prefer ids from the list endpoints; when those are refused, fall back
	// to the container's image id, the default "bridge" network and a volume
	// named in an inspect, so the per-object endpoints are still probed.
	var imgs []struct{ Id string }
	if json.Unmarshal(imgBody, &imgs) == nil && len(imgs) > 0 {
		i.image = imgs[0].Id
	}
	i.network = "bridge"
	var nets []struct{ Id, Name string }
	_ = json.Unmarshal(netBody, &nets)
	for _, n := range nets {
		if n.Name == "bridge" {
			i.network = n.Id
		}
	}
	i.volume = volume
	var vols struct{ Volumes []struct{ Name string } }
	if json.Unmarshal(volBody, &vols) == nil && len(vols.Volumes) > 0 {
		i.volume = vols.Volumes[0].Name
	}

	var rows []ProbeRow
	for _, p := range probes {
		row := ProbeRow{Method: p.method, Path: p.tmpl, Group: p.group, RawExpect: p.rawExpect}
		path := p.path(i)
		if (i.container == "" && strings.Contains(p.tmpl, "{id}")) || (i.image == "" && strings.Contains(p.tmpl, "{image}")) ||
			(i.network == "" && strings.Contains(p.tmpl, "{network}")) || (i.volume == "" && strings.Contains(p.tmpl, "{volume}")) {
			row.Error = "skipped: nothing to probe with"
			rows = append(rows, row)
			continue
		}
		method := http.MethodGet
		if p.method == "HEAD" {
			method = http.MethodHead
		}
		resp, _, err := e.get(method, path, "probe "+p.method+" "+p.tmpl, 25*time.Second)
		if err != nil {
			row.Error = err.Error()
		} else {
			row.Status = resp.StatusCode
			if mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err == nil {
				row.ContentType = mt
			}
		}
		rows = append(rows, row)
	}
	return rows
}
