package correlate

import (
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"dash.cha.sh/internal/graph"
)

// webURL is an http(s) URL reduced to what identifies a service: host, port
// and path. The host is lower-cased without a trailing dot, the port is the
// scheme's when the URL has none, and the path has no trailing slash ("" for
// the root). Query and fragment are dropped.
type webURL struct {
	host string
	port int
	path string
}

func parseURL(s string) (webURL, bool) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.Host == "" || u.User != nil {
		return webURL{}, false
	}
	var port int
	switch strings.ToLower(u.Scheme) {
	case "http":
		port = 80
	case "https":
		port = 443
	default:
		return webURL{}, false
	}
	host := normHost(u.Hostname())
	if host == "" {
		return webURL{}, false
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 || n > 65535 {
			return webURL{}, false
		}
		port = n
	}
	return webURL{host: host, port: port, path: strings.TrimRight(u.EscapedPath(), "/")}, true
}

// addr is the URL's "host:port".
func (u webURL) addr() string { return joinAddr(u.host, u.port) }

// key is the URL without its scheme: "host:port/path".
func (u webURL) key() string { return u.addr() + u.path }

// under reports whether u is p or below it: same address, and u's path is
// p's or starts with it at a segment boundary.
func (u webURL) under(p webURL) bool {
	if u.addr() != p.addr() {
		return false
	}
	return p.path == "" || u.path == p.path || strings.HasPrefix(u.path, p.path+"/")
}

func normHost(h string) string { return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".") }

func joinAddr(host string, port int) string { return net.JoinHostPort(host, strconv.Itoa(port)) }

// parseAddr normalises "host:port" (IPv6 in brackets). The port must be a
// number.
func parseAddr(s string) (string, bool) {
	h, p, err := net.SplitHostPort(strings.TrimSpace(s))
	if err != nil {
		return "", false
	}
	n, err := strconv.Atoi(p)
	if err != nil || n <= 0 || n > 65535 || normHost(h) == "" {
		return "", false
	}
	return joinAddr(normHost(h), n), true
}

// routable reports whether host is an IP address that names one machine on
// the network: not loopback ("127.0.0.1" is every machine), unspecified,
// link-local or multicast. Private addresses are routable.
func routable(host string) bool {
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return !(ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast())
}

// isIP reports whether host is a literal address.
func isIP(host string) bool {
	_, err := netip.ParseAddr(host)
	return err == nil
}

// usableHost is a host other sources can name the same way: a routable IP,
// or a DNS name that isn't localhost.
func usableHost(host string) bool {
	if host == "" {
		return false
	}
	if isIP(host) {
		return routable(host)
	}
	return host != "localhost" && !strings.HasSuffix(host, ".localhost")
}

// binding is one Docker port binding: "18989->8989/tcp" or
// "10.0.11.120:8080->80/tcp".
type binding struct {
	hostIP   string // "" for a wildcard
	hostPort int
	port     int
	proto    string
}

func parseBinding(s string) (binding, bool) {
	left, right, ok := strings.Cut(s, "->")
	if !ok {
		return binding{}, false
	}
	var b binding
	if strings.Contains(left, ":") {
		h, p, err := net.SplitHostPort(left)
		if err != nil {
			return binding{}, false
		}
		b.hostIP = normHost(h)
		left = p
	}
	n, err := strconv.Atoi(left)
	if err != nil || n <= 0 {
		return binding{}, false
	}
	b.hostPort = n
	cp, ok := parsePort(right)
	if !ok {
		return binding{}, false
	}
	b.port, b.proto = cp.port, cp.proto
	return b, true
}

type portProto struct {
	port  int
	proto string
}

// parsePort reads "8989/tcp" (tcp when the protocol is missing).
func parsePort(s string) (portProto, bool) {
	p, proto, _ := strings.Cut(s, "/")
	n, err := strconv.Atoi(p)
	if err != nil || n <= 0 || n > 65535 {
		return portProto{}, false
	}
	if proto == "" {
		proto = "tcp"
	}
	return portProto{n, strings.ToLower(proto)}, true
}

// Attribute readers. Attrs are decoded JSON: strings, float64, []any, maps.

func str(r *graph.Resource, key string) string {
	s, _ := r.Attrs[key].(string)
	return s
}

func strList(r *graph.Resource, key string) []string {
	switch v := r.Attrs[key].(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return v
	case string:
		if v != "" {
			return []string{v}
		}
	}
	return nil
}

func num(r *graph.Resource, key string) (float64, bool) {
	switch v := r.Attrs[key].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	}
	return 0, false
}

// sized: the engine sees the same cores as the host or guest, and its
// memory within 15% below it (a VM's kernel keeps some of what it's given).
func sized(engine, host *graph.Resource) bool {
	ec, ok1 := num(engine, "cores")
	hc, ok2 := num(host, "cores")
	em, ok3 := num(engine, "mem_gb")
	hm, ok4 := num(host, "mem_gb")
	if !ok1 || !ok2 || !ok3 || !ok4 || ec <= 0 || hm <= 0 {
		return false
	}
	return ec == hc && em <= hm+0.1 && em >= hm*0.85
}

// links are the resource's link URLs (attrs.links, as the emitter writes them).
func links(r *graph.Resource) []string {
	var out []string
	if ls, ok := r.Attrs["links"].([]any); ok {
		for _, l := range ls {
			if m, ok := l.(map[string]any); ok {
				if u, ok := m["url"].(string); ok && u != "" {
					out = append(out, u)
				}
			}
		}
	}
	return out
}

// integrationOf is the integration a resource ID belongs to: "docker" for
// "docker:i_x/container/sonarr".
func integrationOf(id graph.RID) string {
	s, _, _ := strings.Cut(string(id), ":")
	return s
}

// instanceOf is the integration instance behind a resource, from its source
// ("integration:i_x" → "i_x"), or "".
func instanceOf(r *graph.Resource) string {
	s, ok := strings.CutPrefix(r.Source, "integration:")
	if !ok {
		return ""
	}
	return s
}

// tokens splits a name into lower-case alphanumeric runs: "binhex/arch-sonarr"
// → [binhex arch sonarr].
func tokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
}

// compact is a name without separators or case: "Radarr HD" → "radarrhd".
func compact(s string) string { return strings.Join(tokens(s), "") }
