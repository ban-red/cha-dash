package proxmox

import (
	"fmt"
	"slices"
	"strings"

	"dash.cha.sh/internal/integration"
)

// privileges is GET /access/permissions without parameters: the token's own
// effective privileges, path → privilege → propagate flag. PVE lists every
// path where a privilege takes effect, so a role on / shows on /vms too.
type privileges map[string]map[string]num

// has reports whether priv is held on path, directly or propagated from an
// ancestor.
func (p privileges) has(path, priv string) bool {
	for q := path; ; q = parentPath(q) {
		if v, ok := p[q][priv]; ok && (q == path || v.bool()) {
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

// access classifies a PVE privilege. Only auditing is read-only; everything
// else can change something (VM.Monitor and the console ones can change
// anything), and a few are admin-equivalent.
func access(priv string) string {
	switch {
	case strings.HasSuffix(priv, ".Audit"), priv == "Sys.Syslog", priv == "VM.GuestAgent.FileRead":
		return integration.AccessRead
	case priv == "Permissions.Modify", priv == "Sys.Console", priv == "User.Modify",
		priv == "Realm.Allocate", priv == "Realm.AllocateUser":
		return integration.AccessAdmin
	}
	return integration.AccessWrite
}

// grants turns the permissions into one grant per privilege, naming the
// paths it's held on. Anything beyond auditing is Write.
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
		out = append(out, integration.Grant{Name: n, Access: a, Write: a != integration.AccessRead, Detail: "on " + joinCapped(ps, 8)})
	}
	return out
}

func joinCapped(s []string, n int) string {
	if len(s) <= n {
		return strings.Join(s, ", ")
	}
	return strings.Join(s[:n], ", ") + fmt.Sprintf(" and %d more", len(s)-n)
}

// readWarnings say what this token can't show, judged from its privileges:
// what it needs for each part of the integration, and what a read-only
// token can never see (pending updates, backup volumes).
func (p privileges) readWarnings() []integration.Warning {
	var ws []integration.Warning
	missing := func(code, msg string) {
		ws = append(ws, integration.Warning{Code: code, Severity: integration.WarnWarning, Message: msg})
	}
	if !p.has("/vms", "VM.Audit") {
		missing("guests_hidden", "Guests aren't visible: the token lacks VM.Audit on /vms. Grant it PVEAuditor on /.")
	}
	if !p.has("/nodes", "Sys.Audit") {
		missing("nodes_hidden", "Cluster status, tasks and backup jobs aren't visible: the token lacks Sys.Audit. Grant it PVEAuditor on /.")
	}
	if !p.has("/storage", "Datastore.Audit") {
		missing("storage_hidden", "Storage isn't visible: the token lacks Datastore.Audit on /storage. Grant it PVEAuditor on /.")
	}
	if !p.has("/nodes", "Sys.Modify") {
		ws = append(ws, integration.Warning{Code: "updates_hidden", Severity: integration.WarnInfo,
			Message: "Pending updates aren't shown: reading them needs Sys.Modify, which can also change the hosts. A read-only token is the safer choice."})
	}
	if !p.has("/storage", "Datastore.AllocateSpace") || !p.has("/vms", "VM.Backup") {
		ws = append(ws, integration.Warning{Code: "backups_from_tasks", Severity: integration.WarnInfo,
			Message: "A read-only token can't list backup volumes, so backup results come from the vzdump tasks and their logs. Add a PBS instance to see the backups themselves."})
	}
	return ws
}
