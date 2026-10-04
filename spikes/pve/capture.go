package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Capture walks one instance with GETs only and writes the raw bodies to
// rawDir/<instance>/. Raw captures hold real names and addresses: they stay
// git-ignored and are only ever read by the sanitizer.

// RawMeta is rawDir/<instance>/_meta.json.
type RawMeta struct {
	Instance   string     `json:"instance"`
	CapturedAt time.Time  `json:"captured_at"`
	TLS        TLSFacts   `json:"tls"`
	Requests   []RawEntry `json:"requests"`
	WallMS     float64    `json:"wall_ms"`
}

// RawEntry is one request and the file its body went to.
type RawEntry struct {
	Response
	File string `json:"file"`
}

type recorder struct {
	ctx  context.Context
	c    *Client
	dir  string
	meta *RawMeta
	log  func(format string, a ...any)
}

// get records path into file (relative to the instance dir). An empty file
// name is derived from the path without its query.
func (r *recorder) get(path, file string) Response {
	resp := r.c.Get(r.ctx, path)
	if file == "" {
		p, _, _ := strings.Cut(path, "?")
		file = strings.TrimPrefix(p, "/") + ".json"
	}
	if resp.Body != nil {
		full := filepath.Join(r.dir, file)
		_ = os.MkdirAll(filepath.Dir(full), 0o700)
		_ = os.WriteFile(full, resp.Body, 0o600)
	}
	r.meta.Requests = append(r.meta.Requests, RawEntry{Response: resp, File: file})
	status := fmt.Sprint(resp.Status)
	if resp.Err != "" {
		status = "ERR " + resp.Err
	}
	r.log(" %-4s %7.1f ms %8d B  %s", status, resp.LatencyMS, resp.Bytes, templ(path))
	return resp
}

type nodeInfo struct {
	Name   string
	Online bool
}

type guestInfo struct {
	Node, Type string
	VMID       int
	Status     string
	Template   bool
}

type storageInfo struct {
	Node, ID, Status string
	Shared, Enabled  bool
}

func capture(ctx context.Context, in Instance, store *TOFUStore, allowNew bool, rawDir string, delay, timeout time.Duration, debug bool) (*RawMeta, error) {
	dir := filepath.Join(rawDir, in.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	c := newClient(in, store, allowNew, delay, timeout)
	c.Debug = debug
	meta := &RawMeta{Instance: in.Name, CapturedAt: time.Now().UTC()}
	r := &recorder{ctx: ctx, c: c, dir: dir, meta: meta, log: func(f string, a ...any) {
		fmt.Fprintf(os.Stderr, "[%s]"+f+"\n", append([]any{in.Name}, a...)...)
	}}
	start := time.Now()
	defer func() { meta.WallMS = ms(time.Since(start)); meta.TLS = c.TLS }()

	v := r.get("/version", "")
	if v.Err != "" || v.Status != 200 {
		meta.TLS = c.TLS
		return meta, fmt.Errorf("%s: /version failed (%d %s %s)", in.Name, v.Status, v.Reason, v.Err)
	}

	// Cluster shape. /cluster/status lists a "cluster" entry only when clustered,
	// plus one "node" entry per member (with online/local/nodeid/ip).
	var nodes []nodeInfo
	cs := r.get("/cluster/status", "")
	var csData []map[string]any
	if cs.Status == 200 && dataOf(cs.Body, &csData) == nil {
		for _, e := range csData {
			if e["type"] == "node" {
				nodes = append(nodes, nodeInfo{Name: str(e["name"]), Online: num(e["online"]) == 1})
			}
		}
	}
	nl := r.get("/nodes", "")
	if len(nodes) == 0 {
		var nd []map[string]any
		if nl.Status == 200 && dataOf(nl.Body, &nd) == nil {
			for _, e := range nd {
				nodes = append(nodes, nodeInfo{Name: str(e["node"]), Online: e["status"] == "online"})
			}
		}
	}

	// Everything in one call: nodes, guests, storage, sdn, pools.
	var guests []guestInfo
	var storages []storageInfo
	cr := r.get("/cluster/resources", "")
	var crData []map[string]any
	if cr.Status == 200 && dataOf(cr.Body, &crData) == nil {
		for _, e := range crData {
			switch e["type"] {
			case "qemu", "lxc":
				guests = append(guests, guestInfo{Node: str(e["node"]), Type: str(e["type"]), VMID: int(num(e["vmid"])),
					Status: str(e["status"]), Template: num(e["template"]) == 1})
			case "storage":
				storages = append(storages, storageInfo{Node: str(e["node"]), ID: str(e["storage"]), Status: str(e["status"]),
					Shared: num(e["shared"]) == 1})
			}
		}
	}
	sort.Slice(guests, func(i, j int) bool { return guests[i].VMID < guests[j].VMID })

	r.get("/cluster/tasks", "")
	r.get("/cluster/backup", "")
	r.get("/cluster/backup-info/not-backed-up", "")
	r.get("/cluster/ha/status/current", "")
	r.get("/cluster/config/join", "")
	r.get("/cluster/metrics/export", "")
	r.get(fmt.Sprintf("/cluster/metrics/export?history=1&start-time=%d", time.Now().Add(-10*time.Minute).Unix()), "cluster/metrics/export.history-10m.json")
	r.get("/pools", "")
	r.get("/storage", "")

	// What can this token actually do?
	r.get("/access/permissions", "")
	r.get("/access/permissions?userid="+url.QueryEscape(in.User()), "access/permissions.owner.json")
	r.get("/access/users/"+url.PathEscape(in.User())+"/token/"+url.PathEscape(in.Token()), "access/token.json")
	r.get("/access/users/"+url.PathEscape(in.User()), "access/user.json") // tokens{privsep} if readable
	r.get("/access/roles", "")
	r.get("/access/acl", "")

	onlineNode := map[string]bool{}
	for _, n := range nodes {
		onlineNode[n.Name] = n.Online
		if !n.Online {
			r.log("  node %d offline, skipped", len(onlineNode))
			continue
		}
		p := "/nodes/" + url.PathEscape(n.Name)
		r.get(p+"/status", "")
		tasks := r.get(p+"/tasks?limit=50&source=all", "")
		r.get(p+"/storage", "")
		r.get(p+"/rrddata?timeframe=hour&cf=AVERAGE", "nodes/"+n.Name+"/rrddata.hour.json")
		r.get(p+"/rrddata?timeframe=day&cf=AVERAGE", "nodes/"+n.Name+"/rrddata.day.json")
		r.get(p+"/disks/list", "")
		r.get(p+"/apt/update", "")
		r.get(p+"/certificates/info", "")

		// Backups as an auditor: the newest vzdump task, and the newest failed
		// one, with their logs (per-guest results only live in the log).
		var tl []map[string]any
		if tasks.Status == 200 && dataOf(tasks.Body, &tl) == nil {
			var newest, failed string
			for _, t := range tl { // newest first
				if t["type"] != "vzdump" || t["endtime"] == nil {
					continue
				}
				if newest == "" {
					newest = str(t["upid"])
				}
				if failed == "" && str(t["status"]) != "OK" {
					failed = str(t["upid"])
				}
			}
			for i, upid := range []string{newest, failed} {
				if upid == "" || (i == 1 && upid == newest) {
					continue
				}
				name := []string{"vzdump-newest", "vzdump-failed"}[i]
				tp := p + "/tasks/" + url.PathEscape(upid)
				r.get(tp+"/status", "nodes/"+n.Name+"/tasks/"+name+".status.json")
				r.get(tp+"/log?limit=1000", "nodes/"+n.Name+"/tasks/"+name+".log.json")
			}
		}
	}

	// Storage contents: shared storage once, local storage per node.
	seenShared := map[string]bool{}
	for _, s := range storages {
		if s.Status != "available" || !onlineNode[s.Node] {
			continue
		}
		if s.Shared {
			if seenShared[s.ID] {
				continue
			}
			seenShared[s.ID] = true
		}
		r.get("/nodes/"+url.PathEscape(s.Node)+"/storage/"+url.PathEscape(s.ID)+"/content", "")
	}

	// Guests: config (onboot = desired state), addresses where readable, and
	// one RRD sample to see the per-guest history shape.
	sampled := false
	for _, g := range guests {
		if !onlineNode[g.Node] {
			continue
		}
		p := fmt.Sprintf("/nodes/%s/%s/%d", url.PathEscape(g.Node), g.Type, g.VMID)
		cfg := r.get(p+"/config", "")
		if g.Status != "running" {
			continue
		}
		if g.Type == "lxc" {
			r.get(p+"/interfaces", "")
		} else {
			var conf map[string]any
			if cfg.Status == 200 && dataOf(cfg.Body, &conf) == nil {
				if a := str(conf["agent"]); strings.HasPrefix(a, "1") || strings.Contains(a, "enabled=1") {
					r.get(p+"/agent/network-get-interfaces", "")
				}
			}
		}
		if !sampled {
			sampled = true
			r.get(p+"/rrddata?timeframe=hour&cf=AVERAGE", fmt.Sprintf("nodes/%s/%s/%d/rrddata.hour.json", g.Node, g.Type, g.VMID))
		}
	}
	return meta, nil
}

// templ makes a path safe to print: node, storage, user and token segments
// become placeholders. VMIDs and endpoint names stay.
func templ(path string) string {
	p, q, _ := strings.Cut(path, "?")
	seg := strings.Split(p, "/")
	for i := 1; i < len(seg); i++ {
		switch seg[i-1] {
		case "nodes":
			seg[i] = "{node}"
		case "storage":
			if i >= 3 && seg[i-3] == "nodes" {
				seg[i] = "{storage}"
			}
		case "users":
			seg[i] = "{user}"
		case "token":
			seg[i] = "{token}"
		case "tasks":
			seg[i] = "{upid}"
		}
	}
	out := strings.Join(seg, "/")
	if q != "" {
		if strings.Contains(q, "userid=") {
			q = "userid={user}"
		}
		out += "?" + q
	}
	return out
}
