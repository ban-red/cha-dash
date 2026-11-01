package correlate

import (
	"cmp"
	"maps"
	"path"
	"slices"
	"strings"

	"dash.cha.sh/internal/graph"
)

// BackedUpBy is coverage: a protectable resource (a guest, a container
// with volumes, an Unraid share or flash drive) → what backs it up.
// Proxmox publishes guest → backup job itself; the correlator adds what a
// backup tool's target says it covers (rule "covers").
const BackedUpBy graph.RelType = "backed_up_by"

// CapBackupTargets marks a resource that reports backups it makes: a PBS
// datastore, or a spec's target (docs/specs.md, "Backup targets").
const CapBackupTargets = "backups.targets"

// AttrCovers is a target's list of what it backs up. Each entry is an
// object, {"host", "path"} or {"resource"}, or a string meaning a host.
const AttrCovers = "backup.covers"

const maxCovers = 32

type coverEntry struct{ host, path, resource string }

// covers joins backup targets to what they back up, from each target's
// backup.covers: a resource ID as is (exact); a host by its address
// (strong) or its name when only one host or guest has it (strong; a name
// two share joins nothing: coverage is never claimed on a guess); a path on that host to the Unraid shares under
// /mnt/user, its flash at /boot, and the containers whose every mount is
// under it. Without a path the host or guest itself is covered.
func (w *world) covers() {
	var targets []*graph.Resource
	for _, k := range slices.Sorted(maps.Keys(w.kinds)) {
		for _, r := range w.kinds[k] {
			if w.isPrimary(r) && hasCap(r, CapBackupTargets) && len(coversOf(r)) > 0 {
				targets = append(targets, r)
			}
		}
	}
	var cs []cand
	for _, t := range targets {
		for _, e := range coversOf(t) {
			what := cmp.Or(e.resource, e.host)
			if e.path != "" {
				what += ":" + e.path
			}
			why := w.name(t.ID) + " backs up " + what
			if e.resource != "" {
				if r := w.res[graph.RID(e.resource)]; r != nil {
					cs = append(cs, w.rel(r.ID, BackedUpBy, t.ID, "covers", Exact, "", scoreUser, why+", which names it."))
				}
				continue
			}
			for _, h := range w.hostsNamed(e.host) {
				on := why + ", on " + w.name(h)
				for _, c := range w.underPath(h, e.path) {
					cs = append(cs, w.rel(c, BackedUpBy, t.ID, "covers", Strong, "", scoreAddress, on+"."))
				}
			}
		}
	}
	w.resolve(cs)
}

// hostsNamed are the hosts and guests at an address, or the one with a
// name. A name several share names none.
func (w *world) hostsNamed(host string) []graph.RID {
	host = normHost(host)
	if host == "" {
		return nil
	}
	if routable(host) {
		return w.ips[host]
	}
	short, _, _ := strings.Cut(host, ".")
	var out []graph.RID
	for _, kind := range []graph.Kind{"host", "vm", "lxc"} {
		for _, r := range w.primaries(kind) {
			for _, m := range w.members(r.ID) {
				n := strings.ToLower(cmp.Or(str(m, "hostname"), m.Name))
				if n != "" && (n == host || n == short) && !slices.Contains(out, r.ID) {
					out = append(out, r.ID)
				}
			}
		}
	}
	if len(out) != 1 {
		return nil
	}
	return out
}

// underPath is what a backup of p on host h covers: h itself for no path
// (or /); the shares under /mnt/user or /mnt/user0 and the flash at /boot
// of an Unraid host; the containers on h whose every mount is under p.
func (w *world) underPath(h graph.RID, p string) []graph.RID {
	p = strings.TrimSpace(p)
	if p == "" || p == "/" {
		return []graph.RID{h}
	}
	p = path.Clean(p)
	var out []graph.RID
	add := func(id graph.RID) {
		if id = w.canon(id); !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	onHost := map[graph.RID]bool{}
	for _, m := range w.members(h) {
		onHost[m.ID] = true
		for _, a := range w.sources(m.ID, "part_of", "array") {
			onHost[a.ID] = true
		}
	}
	for _, s := range w.primaries("share") {
		if !w.partOfAny(s.ID, onHost) {
			continue
		}
		for _, root := range []string{"/mnt/user", "/mnt/user0"} {
			if p == root || p == "/mnt" || p == root+"/"+s.Name {
				add(s.ID)
			}
		}
	}
	if p == "/boot" {
		for _, d := range w.primaries("disk") {
			if str(d, "role") == "boot" && w.partOfAny(d.ID, onHost) {
				add(d.ID)
			}
		}
	}
	for _, e := range w.primaries("engine") {
		if t, ok := w.place[e.ID]; !ok || w.canon(t) != w.canon(h) {
			continue
		}
		for _, em := range w.members(e.ID) {
			for _, c := range w.sources(em.ID, RunsOn, "container") {
				if srcs := mountSources(w.members(c.ID)); len(srcs) > 0 && allUnder(srcs, p) {
					add(c.ID)
				}
			}
		}
	}
	return out
}

func (w *world) partOfAny(id graph.RID, set map[graph.RID]bool) bool {
	for _, m := range w.members(id) {
		for _, t := range w.targets(m.ID, "part_of") {
			if set[t.ID] || set[w.canon(t.ID)] {
				return true
			}
		}
	}
	return false
}

// mountSources are the host paths a container's mounts read from (the
// Docker integration's mounts attr), across its members.
func mountSources(members []*graph.Resource) []string {
	var out []string
	for _, m := range members {
		ms, _ := m.Attrs["mounts"].([]any)
		for _, x := range ms {
			if mm, ok := x.(map[string]any); ok {
				if s, _ := mm["source"].(string); strings.HasPrefix(s, "/") {
					out = append(out, path.Clean(s))
				}
			}
		}
	}
	return out
}

func allUnder(paths []string, root string) bool {
	for _, s := range paths {
		if s != root && !strings.HasPrefix(s, strings.TrimSuffix(root, "/")+"/") {
			return false
		}
	}
	return true
}

func hasCap(r *graph.Resource, c string) bool {
	return slices.Contains(strList(r, "caps"), c)
}

// coversOf reads a target's backup.covers, at most maxCovers entries.
func coversOf(r *graph.Resource) []coverEntry {
	raw, _ := r.Attrs[AttrCovers].([]any)
	var out []coverEntry
	for _, x := range raw {
		if len(out) == maxCovers {
			break
		}
		switch v := x.(type) {
		case string:
			if v != "" {
				out = append(out, coverEntry{host: v})
			}
		case map[string]any:
			e := coverEntry{}
			e.host, _ = v["host"].(string)
			e.path, _ = v["path"].(string)
			e.resource, _ = v["resource"].(string)
			if e.host != "" || e.resource != "" {
				out = append(out, e)
			}
		}
	}
	return out
}
