package main

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
)

// report prints a sanitized analysis of the fixtures: it only ever reads -out,
// so everything it prints is already public. The README's tables come from it.

var instanceOrder = []string{"pluto", "minig5", "mars", "io", "jupiter-nas"}

func report(outDir string, w io.Writer) error {
	var names []string
	for _, n := range instanceOrder {
		if fileExists(filepath.Join(outDir, n, "_index.json")) {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return fmt.Errorf("no fixtures in %s", outDir)
	}
	L := func(inst, rel string) any { return loadData(filepath.Join(outDir, inst, rel)) }

	fmt.Fprintln(w, "## Instances")
	fmt.Fprintln(w, "| Instance | PVE | Shape | Nodes | Guests (running/total) | Storage (distinct) | TLS |")
	fmt.Fprintln(w, "|---|---|---|---|---|---|---|")
	for _, n := range names {
		v, _ := L(n, "version.json").(map[string]any)
		shape, nodes := "standalone", []string{}
		for _, e := range list(L(n, "cluster/status.json")) {
			if e["type"] == "cluster" {
				shape = fmt.Sprintf("cluster %q (%s nodes, quorate %s)", str(e["name"]), str(e["nodes"]), str(e["quorate"]))
			}
			if e["type"] == "node" {
				nodes = append(nodes, str(e["name"]))
			}
		}
		run, tot := 0, 0
		stor := map[string]bool{}
		for _, e := range list(L(n, "cluster/resources.json")) {
			switch e["type"] {
			case "qemu", "lxc":
				tot++
				if e["status"] == "running" {
					run++
				}
			case "storage":
				stor[str(e["storage"])] = true
			}
		}
		var t TLSReport
		_ = readJSON(filepath.Join(outDir, n, "_tls.json"), &t)
		fmt.Fprintf(w, "| %s | %s | %s | %s | %d/%d | %d | %s, SAN matches URL host: %v |\n", n, str(v["version"]), shape,
			strings.Join(nodes, ", "), run, tot, len(stor), t.FirstMode, t.SANMatchesHost)
	}

	fmt.Fprintln(w, "\n## Privilege separation")
	fmt.Fprintln(w, "| Instance | privsep | Token ACL | Effective privileges (token) | Beyond PVEAuditor | Owner user's privileges |")
	fmt.Fprintln(w, "|---|---|---|---|---|---|")
	for _, n := range names {
		auditor := map[string]bool{}
		for _, r := range list(L(n, "access/roles.json")) {
			if r["roleid"] == "PVEAuditor" {
				for _, p := range strings.Split(str(r["privs"]), ",") {
					auditor[p] = true
				}
			}
		}
		privs := func(rel string) (map[string]bool, string) {
			out := map[string]bool{}
			pm, _ := L(n, rel).(map[string]any)
			for _, v := range pm {
				for p := range v.(map[string]any) {
					out[p] = true
				}
			}
			return out, strings.Join(sortedKeys(out), " ")
		}
		tok, tokS := privs("access/permissions.json")
		_, ownS := privs("access/permissions.owner.json")
		var beyond []string
		for p := range tok {
			if !auditor[p] {
				beyond = append(beyond, p)
			}
		}
		sort.Strings(beyond)
		ps := "?"
		if u, ok := L(n, "access/user.json").(map[string]any); ok {
			if ts, ok := u["tokens"].(map[string]any); ok {
				for _, t := range ts {
					ps = str(t.(map[string]any)["privsep"])
				}
			}
		}
		var acl []string
		for _, a := range list(L(n, "access/acl.json")) {
			acl = append(acl, fmt.Sprintf("%s %s %s", str(a["type"]), str(a["roleid"]), str(a["path"])))
		}
		b := "none"
		if len(beyond) > 0 {
			b = "**" + strings.Join(beyond, " ") + "**"
		}
		same := ownS
		if ownS == tokS {
			same = "same as token"
		}
		fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s |\n", n, ps, strings.Join(acl, "; "), tokS, b, same)
	}

	fmt.Fprintln(w, "\n## Requests per instance (one full capture)")
	fmt.Fprintln(w, "| Instance | Requests | Non-200 | Sum of latencies | Wall | Decoded | On the wire |")
	fmt.Fprintln(w, "|---|---|---|---|---|---|---|")
	type ep struct {
		ms          []float64
		bytes, wire []int
	}
	per := map[string]*ep{}
	for _, n := range names {
		var idx Index
		if err := readJSON(filepath.Join(outDir, n, "_index.json"), &idx); err != nil {
			return err
		}
		non, sum, by, wb := 0, 0.0, 0, 0
		for _, r := range idx.Requests {
			if r.Status != 200 {
				non++
			}
			sum += r.MS
			by += r.Bytes
			wb += r.WireBytes
			k := templ(r.Path)
			if strings.HasPrefix(k, "/cluster/metrics/export?history") {
				k = "/cluster/metrics/export?history=1&start-time=now-10m"
			}
			if per[k] == nil {
				per[k] = &ep{}
			}
			per[k].ms = append(per[k].ms, r.MS)
			per[k].bytes = append(per[k].bytes, r.Bytes)
			per[k].wire = append(per[k].wire, r.WireBytes)
		}
		fmt.Fprintf(w, "| %s | %d | %d | %.0f ms | %.1f s | %s | %s |\n", n, len(idx.Requests), non, sum, idx.WallMS/1000, kb(by), kb(wb))
	}
	fmt.Fprintln(w, "\n## Per endpoint (across instances: median latency, max decoded size, max wire size)")
	fmt.Fprintln(w, "| Endpoint | Calls | Median | Max decoded | Max wire |")
	fmt.Fprintln(w, "|---|---|---|---|---|")
	for _, k := range sortedKeys(per) {
		e := per[k]
		sort.Float64s(e.ms)
		fmt.Fprintf(w, "| `%s` | %d | %.0f ms | %s | %s |\n", k, len(e.ms), e.ms[len(e.ms)/2], kb(maxInt(e.bytes)), kb(maxInt(e.wire)))
	}

	fmt.Fprintln(w, "\n## Backups")
	for _, n := range names {
		var jobs []string
		for _, j := range list(L(n, "cluster/backup.json")) {
			jobs = append(jobs, fmt.Sprintf("%q → %s, vmid %s, enabled %s", str(j["schedule"]), str(j["storage"]), str(j["vmid"]), str(j["enabled"])))
		}
		var nbu []string
		for _, g := range list(L(n, "cluster/backup-info/not-backed-up.json")) {
			nbu = append(nbu, fmt.Sprintf("%s/%s %s", str(g["type"]), str(g["vmid"]), str(g["name"])))
		}
		ok, bad := 0, 0
		for _, t := range list(L(n, "cluster/tasks.json")) {
			if t["type"] == "vzdump" {
				if t["status"] == "OK" {
					ok++
				} else {
					bad++
				}
			}
		}
		fmt.Fprintf(w, "- **%s**: jobs: %s. Not covered: %s. Recent vzdump tasks: %d OK, %d failed.\n", n, orNone(jobs), orNone(nbu), ok, bad)
	}
	return nil
}

func orNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, "; ")
}

func maxInt(a []int) int {
	m := 0
	for _, x := range a {
		m = max(m, x)
	}
	return m
}

func kb(b int) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f kB", float64(b)/(1<<10))
	}
	return fmt.Sprintf("%d B", b)
}
