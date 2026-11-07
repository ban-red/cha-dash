// Package registry is how the node talks to image registries: the logins
// it may use, read from a Docker config file on its host (plan 16, private
// registries), and a HEAD for a tag's manifest digest, kept for a while
// (Checker). The docker module (image.check, pulls) and the stacks module
// (stack.update.check, deploy pulls) share it.
//
// Credentials never leave the node: they go to the registry they belong
// to, or to the Docker Engine for a pull, and nowhere else. A Login prints
// and marshals as "[redacted]", so one that slips into a log line or a
// reply says nothing.
package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
)

// DefaultConfigPath is where the node looks for the Docker config file
// holding its registry logins: the host's ~/.docker/config.json, mounted.
const DefaultConfigPath = "/etc/chadash-node/docker-config.json"

// maxConfig bounds the config file the node reads.
const maxConfig = 1 << 20

// Login is a registry's user and password. It never prints.
type Login struct {
	Username string
	Password string
}

const redacted = "[redacted]"

func (Login) String() string               { return redacted }
func (Login) GoString() string             { return redacted }
func (Login) LogValue() slog.Value         { return slog.StringValue(redacted) }
func (Login) MarshalJSON() ([]byte, error) { return json.Marshal(redacted) }
func (l Login) basic() string {
	return base64.StdEncoding.EncodeToString([]byte(l.Username + ":" + l.Password))
}
func (l Login) empty() bool                  { return l.Username == "" && l.Password == "" }
func (l Login) sameAs(o Login) bool          { return l.Username == o.Username && l.Password == o.Password }
func (l Login) withServer(s string) authBody { return authBody{l.Username, l.Password, s} }

// authBody is the X-Registry-Auth header's JSON.
type authBody struct {
	Username      string `json:"username"`
	Password      string `json:"password"`
	ServerAddress string `json:"serveraddress"`
}

// Logins are the registry logins the node may use: those in its Docker
// config file for the registries the policy's images.registries lists.
// A login for any other registry is never sent. The file is read again
// when it changes, so a `docker login` on the host needs no restart. A
// nil *Logins has none.
type Logins struct {
	path    string
	allowed []string // normalised hosts
	log     *slog.Logger

	mu     sync.Mutex
	mod    time.Time
	size   int64
	loaded bool
	byHost map[string]Login
	// A credential helper's logins, asked for by host when the file names
	// one (credHelpers for that host, else credsStore) and kept a while.
	store   string
	helpers map[string]string // normalised host → helper name
	asked   map[string]helped
	now     func() time.Time
	lookup  func(name string) (string, error)
	get     func(ctx context.Context, program, server string) (Login, error)
}

// helped is a credential helper's answer for a host.
type helped struct {
	login Login
	ok    bool
	at    time.Time
}

// helperKeep is how long a helper's answer is used before it's asked again:
// helpers such as ECR's hand out logins that expire.
const helperKeep = 5 * time.Minute

// helperTimeout bounds one call to a credential helper.
const helperTimeout = 10 * time.Second

// NewLogins reads logins from the Docker config file at path, for the
// registries allowed lists (host or host:port). A missing file, or an
// empty allowed list, means no logins.
func NewLogins(path string, allowed []string, log *slog.Logger) *Logins {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	l := &Logins{path: path, log: log, now: time.Now, lookup: exec.LookPath, get: helperGet}
	for _, a := range allowed {
		if h := Host(a); h != "" && !slices.Contains(l.allowed, h) {
			l.allowed = append(l.allowed, h)
		}
	}
	return l
}

// For is the login for a registry ("ghcr.io", "docker.io",
// "registry.lab.example:5000"), when the policy allows it and the file
// has one.
func (l *Logins) For(domain string) (Login, bool) {
	if l == nil {
		return Login{}, false
	}
	h := Host(domain)
	if !slices.Contains(l.allowed, h) {
		return Login{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reload()
	if lg, ok := l.byHost[h]; ok {
		return lg, ok
	}
	return l.fromHelper(h)
}

// helperOf is the credential helper the file names for a host. l.mu is held.
func (l *Logins) helperOf(h string) string {
	if name := l.helpers[h]; name != "" {
		return name
	}
	return l.store
}

// Missing is the credential helper the Docker config names for a registry
// the policy allows, when the node can't run it (it isn't on its PATH), or
// "". The node's image ships no helpers.
func (l *Logins) Missing(domain string) string {
	if l == nil {
		return ""
	}
	h := Host(domain)
	if !slices.Contains(l.allowed, h) {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reload()
	if _, ok := l.byHost[h]; ok {
		return ""
	}
	name := l.helperOf(h)
	if name == "" {
		return ""
	}
	if _, err := l.lookup(helperProgram(name)); err != nil {
		return name
	}
	return ""
}

// Describe adds to a missing-login error the helper the node couldn't run.
func (l *Logins) Describe(e *LoginError) *LoginError {
	if e != nil && !e.Had {
		e.Helper = l.Missing(e.Registry)
	}
	return e
}

// fromHelper asks the host's credential helper, if the file names one and
// the node can run it. l.mu is held; a helper is asked at most every
// helperKeep per host.
func (l *Logins) fromHelper(h string) (Login, bool) {
	name := l.helperOf(h)
	if name == "" {
		return Login{}, false
	}
	if a, ok := l.asked[h]; ok && l.now().Sub(a.at) < helperKeep {
		return a.login, a.ok
	}
	program, err := l.lookup(helperProgram(name))
	if err != nil {
		return Login{}, false // Missing says so
	}
	ctx, cancel := context.WithTimeout(context.Background(), helperTimeout)
	defer cancel()
	lg, err := l.get(ctx, program, ServerAddress(h))
	a := helped{at: l.now()}
	switch {
	case err != nil:
		// Never the helper's output: it could hold the secret.
		l.log.Warn("registry logins: the credential helper didn't give a login", "helper", name, "registry", h)
	case lg.Username == "<token>":
		l.log.Warn("registry logins: the credential helper gave an identity token, which the node doesn't support", "helper", name, "registry", h)
	case !lg.empty():
		a.login, a.ok = lg, true
	}
	if l.asked == nil {
		l.asked = map[string]helped{}
	}
	l.asked[h] = a
	return a.login, a.ok
}

// helperProgram is a credential helper's program: docker-credential-<name>.
func helperProgram(name string) string { return "docker-credential-" + name }

// helperGet runs a credential helper's get: the server address on standard
// input, a JSON {ServerURL, Username, Secret} on standard output.
func helperGet(ctx context.Context, program, server string) (Login, error) {
	cmd := exec.CommandContext(ctx, program, "get") //nolint:gosec // G204: the program is docker-credential-<name> from the node's own PATH, named by the mounted Docker config
	cmd.Stdin = strings.NewReader(server)
	out, err := cmd.Output()
	if err != nil {
		return Login{}, errors.New("the helper failed")
	}
	var c struct {
		Username string `json:"Username"`
		Secret   string `json:"Secret"`
	}
	if json.Unmarshal(out, &c) != nil {
		return Login{}, errors.New("the helper's answer isn't JSON")
	}
	return Login{c.Username, c.Secret}, nil
}

// All is every login the node may use, by host.
func (l *Logins) All() map[string]Login {
	out := map[string]Login{}
	if l == nil {
		return out
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reload()
	for h, lg := range l.byHost {
		if slices.Contains(l.allowed, h) {
			out[h] = lg
		}
	}
	for _, h := range l.allowed {
		if _, ok := out[h]; !ok {
			if lg, ok := l.fromHelper(h); ok {
				out[h] = lg
			}
		}
	}
	return out
}

// EncodedAuth is the X-Registry-Auth value the Docker Engine takes for a
// pull from domain, or "" when the node has no login for it.
func (l *Logins) EncodedAuth(domain string) string {
	lg, ok := l.For(domain)
	if !ok {
		return ""
	}
	b, _ := json.Marshal(lg.withServer(ServerAddress(domain))) //nolint:gosec // G117: X-Registry-Auth carries the login to the local Docker Engine, its only use
	return base64.URLEncoding.EncodeToString(b)
}

// ServerAddress is the key Docker files a registry's login under: Docker
// Hub's is its v1 index URL, every other registry's its host.
func ServerAddress(domain string) string {
	if h := Host(domain); h != "docker.io" {
		return h
	}
	return "https://index.docker.io/v1/"
}

// Host normalises a registry name as the Docker config and image
// references write it: no scheme, no path, lower case, Docker Hub's
// aliases as docker.io.
func Host(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	s, _, _ = strings.Cut(s, "/")
	switch s {
	case "index.docker.io", "registry-1.docker.io", "registry.hub.docker.com":
		return "docker.io"
	}
	return s
}

// configFile is the part of a Docker config file the node reads.
type configFile struct {
	Auths map[string]struct {
		Auth          string `json:"auth"`
		Username      string `json:"username"`
		Password      string `json:"password"`
		IdentityToken string `json:"identitytoken"`
	} `json:"auths"`
	CredsStore  string            `json:"credsStore"`
	CredHelpers map[string]string `json:"credHelpers"`
}

// reload reads the file again when it changed. l.mu is held. Each change
// is logged once, never with a credential in it.
func (l *Logins) reload() {
	st, err := os.Stat(l.path)
	if errors.Is(err, os.ErrNotExist) || l.path == "" {
		if l.loaded && l.byHost != nil {
			l.log.Info("registry logins: the Docker config is gone; no logins are used", "path", l.path)
		}
		l.loaded, l.byHost, l.mod, l.size = true, nil, time.Time{}, 0
		l.store, l.helpers, l.asked = "", nil, nil
		return
	}
	if err != nil {
		if !l.loaded {
			l.log.Warn("registry logins: can't read the Docker config; no logins are used", "path", l.path, "err", err)
		}
		l.loaded, l.byHost = true, nil
		return
	}
	if l.loaded && st.ModTime().Equal(l.mod) && st.Size() == l.size {
		return
	}
	l.loaded, l.mod, l.size, l.byHost = true, st.ModTime(), st.Size(), nil
	l.store, l.helpers, l.asked = "", nil, nil
	f, err := os.Open(l.path)
	if err != nil {
		l.log.Warn("registry logins: can't read the Docker config; no logins are used", "path", l.path, "err", err)
		return
	}
	defer f.Close()
	var cf configFile
	if err := json.NewDecoder(io.LimitReader(f, maxConfig)).Decode(&cf); err != nil {
		// Not the error itself: a decoder can quote what it read.
		l.log.Warn("registry logins: the Docker config isn't valid JSON; no logins are used", "path", l.path)
		return
	}
	l.store, l.helpers, l.asked = cf.CredsStore, map[string]string{}, nil
	for key, name := range cf.CredHelpers {
		if h := Host(key); h != "" && name != "" {
			l.helpers[h] = name
		}
	}
	if cf.CredsStore != "" || len(cf.CredHelpers) > 0 {
		l.log.Info("registry logins: the Docker config uses a credential helper; the node asks it for the registries images.registries lists, when it's on the node's PATH", "path", l.path)
	}
	byHost := map[string]Login{}
	var skipped, unlisted []string
	for key, a := range cf.Auths {
		h := Host(key)
		var lg Login
		switch {
		case a.Username != "" || a.Password != "":
			lg = Login{a.Username, a.Password}
		case a.Auth != "":
			raw, err := base64.StdEncoding.DecodeString(a.Auth)
			if err != nil {
				skipped = append(skipped, h)
				continue
			}
			u, p, ok := strings.Cut(string(raw), ":")
			if !ok || u == "" {
				skipped = append(skipped, h)
				continue
			}
			lg = Login{u, strings.Trim(p, "\x00")}
		default:
			// An entry a credential helper fills, or an identity token.
			skipped = append(skipped, h)
			continue
		}
		if h == "" || lg.empty() {
			continue
		}
		if old, dup := byHost[h]; dup && !old.sameAs(lg) {
			l.log.Warn("registry logins: the Docker config has two different logins for one registry; the node uses neither", "registry", h)
			byHost[h] = Login{}
			continue
		}
		byHost[h] = lg
		if !slices.Contains(l.allowed, h) {
			unlisted = append(unlisted, h)
		}
	}
	for h, lg := range byHost {
		if lg.empty() {
			delete(byHost, h)
		}
	}
	l.byHost = byHost
	slices.Sort(skipped)
	slices.Sort(unlisted)
	if len(skipped) > 0 {
		l.log.Warn("registry logins: some entries have no user and password the node can read (a credential helper's, or an identity token); they're skipped", "registries", strings.Join(skipped, ","))
	}
	if len(unlisted) > 0 {
		l.log.Info("registry logins: images.registries doesn't list these, so their logins are never sent", "registries", strings.Join(unlisted, ","))
	}
	var used []string
	for h := range byHost {
		if slices.Contains(l.allowed, h) {
			used = append(used, h)
		}
	}
	slices.Sort(used)
	l.log.Info("registry logins loaded", "path", l.path, "registries", strings.Join(used, ","))
}
