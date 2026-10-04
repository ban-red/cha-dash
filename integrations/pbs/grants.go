package pbs

import (
	"slices"
	"strings"

	"dash.cha.sh/internal/integration"
)

// privileges is GET /access/permissions: path → privilege → propagate. PBS
// lists every privilege the token holds on "/", "/access", "/datastore",
// "/remote" and "/system" (and the paths it has ACLs on).
type privileges map[string]map[string]num

// has reports whether priv is held on path, directly or propagated from an
// ancestor.
func (p privileges) has(path, priv string) bool {
	for q := path; ; q = parentPath(q) {
		if v, ok := p[q][priv]; ok && (q == path || v != 0) {
			return true
		}
		if q == "/" {
			return false
		}
	}
}

func parentPath(path string) string {
	i := strings.LastIndexByte(path, '/')
	if i <= 0 {
		return "/"
	}
	return path[:i]
}

// access classifies a PBS privilege (pbs-api-types acl.rs). Auditing and
// reading are read-only (Datastore.Read restores backups, so it reads
// everything backed up, but changes nothing); the rest can change things,
// and a few are admin-equivalent.
func access(priv string) string {
	switch {
	case strings.HasSuffix(priv, ".Audit"), priv == "Datastore.Read", priv == "Remote.Read", priv == "Tape.Read":
		return integration.AccessRead
	case priv == "Permissions.Modify", priv == "Sys.Console", priv == "Realm.Allocate":
		return integration.AccessAdmin
	}
	return integration.AccessWrite
}

// grants is one grant per privilege, naming the paths it's held on.
func (p privileges) grants() []integration.Grant {
	paths := map[string][]string{}
	for path, privs := range p {
		for priv := range privs {
			paths[priv] = append(paths[priv], path)
		}
	}
	names := make([]string, 0, len(paths))
	for n := range paths {
		names = append(names, n)
	}
	slices.Sort(names)
	out := make([]integration.Grant, 0, len(names))
	for _, n := range names {
		ps := paths[n]
		slices.Sort(ps)
		a := access(n)
		detail := "on " + strings.Join(ps, ", ")
		if n == "Datastore.Read" {
			detail += "; can read and restore every backup it covers"
		}
		out = append(out, integration.Grant{Name: n, Access: a, Write: a != integration.AccessRead, Detail: detail})
	}
	return out
}

// readWarnings say what the token can't show.
func (p privileges) readWarnings() []integration.Warning {
	var ws []integration.Warning
	if !p.has("/datastore", "Datastore.Audit") {
		ws = append(ws, integration.Warning{Code: "datastores_hidden", Severity: integration.WarnWarning,
			Message: "Datastore usage may be missing: the token lacks Datastore.Audit on /datastore. Grant it the Audit role on /."})
	}
	if !p.has("/system/tasks", "Sys.Audit") {
		ws = append(ws, tasksHidden())
	}
	if !p.has("/system/status", "Sys.Audit") {
		ws = append(ws, integration.Warning{Code: "node_status_hidden", Severity: integration.WarnInfo,
			Message: "The server's CPU and memory aren't shown: that needs Sys.Audit on /system/status (the Audit role on / has it)."})
	}
	return ws
}

func tasksHidden() integration.Warning {
	return integration.Warning{Code: "tasks_hidden", Severity: integration.WarnWarning,
		Message: "Failed tasks aren't counted: without Sys.Audit on /system/tasks PBS shows a token only its own tasks. Grant it the Audit role on /."}
}
