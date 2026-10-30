package stacks

import (
	"net"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// Git remotes as a sync and a hook name them. The node checks a URL again
// before it reaches out (userinfo, scheme, its stacks.remotes); the hub
// refuses the same things first, so an admin hears it while typing.

// checkRemoteURL returns the URL as it will be stored (trimmed), or a
// sentence saying what's wrong with it. Only https, http and ssh URLs: a
// credential never sits in the URL (it goes under auth, into the vault), and
// scp-like "git@host:path" is refused for the same reason.
func checkRemoteURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	switch {
	case raw == "":
		return "", problem("Give the remote's URL.")
	case len(raw) > 2048:
		return "", problem("The URL is too long.")
	case strings.ContainsAny(raw, " \t\r\n"):
		return "", problem("The URL has a space in it.")
	}
	if !strings.Contains(raw, "://") && strings.Contains(raw, "@") {
		return "", problem("Write the URL as ssh://host/path, without a user; the user goes under Auth.")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", problem("That isn't a URL.")
	}
	switch strings.ToLower(u.Scheme) {
	case "https", "http", "ssh":
	default:
		return "", problem("Use an https://, http:// or ssh:// URL.")
	}
	switch {
	case u.User != nil:
		return "", problem("The URL has a user or password in it. Leave them out; the credential goes under Auth, into the vault.")
	case u.Hostname() == "":
		return "", problem("The URL has no host.")
	case u.RawQuery != "" || u.Fragment != "":
		return "", problem("The URL can't have a query or a fragment.")
	case strings.Trim(u.Path, "/") == "":
		return "", problem("The URL has no repository path.")
	}
	return raw, nil
}

// defaultPorts are left out when URLs are compared.
var defaultPorts = map[string]string{"https": "443", "http": "80", "ssh": "22"}

// normalizeURL is how remotes are compared: lowercase scheme and host, no
// default port, no trailing "/" or ".git". Two syncs (or a sync and a hook)
// follow the same remote when their normalized URLs match. Stored sync URLs
// keep their spelling, since a plain git server needs the exact path.
func normalizeURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return strings.ToLower(strings.TrimSpace(raw))
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if p := u.Port(); p != "" && p != defaultPorts[scheme] {
		host = net.JoinHostPort(strings.Trim(host, "[]"), p)
	}
	p := u.Path
	for {
		q := strings.TrimSuffix(strings.TrimSuffix(p, "/"), ".git")
		if q == p {
			break
		}
		p = q
	}
	return scheme + "://" + host + p
}

// remoteHost is the URL's host, for the audit log (never the whole URL).
func remoteHost(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return strings.ToLower(u.Hostname())
	}
	return ""
}

var branchRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/+-]{0,199}$`)

// checkBranch refuses a name git wouldn't take as a branch.
func checkBranch(b string) error {
	if !branchRE.MatchString(b) || strings.Contains(b, "..") || strings.Contains(b, "//") ||
		strings.HasSuffix(b, "/") || strings.HasSuffix(b, ".lock") || strings.HasSuffix(b, ".") {
		return problem("Give a branch name, such as main.")
	}
	return nil
}

// checkPath refuses a path that isn't a clean one inside the repository.
func checkPath(p string) error {
	if p == "" || len(p) > 512 || strings.HasPrefix(p, "/") || path.Clean(p) != p || p == "." ||
		p == ".." || strings.HasPrefix(p, "../") || strings.ContainsAny(p, "\\\x00\r\n") {
		return problem("Give the file's path inside the repository, such as media/compose.yaml.")
	}
	return nil
}
