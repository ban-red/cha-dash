package spec

import (
	"net/url"
	"strings"

	"dash.cha.sh/internal/integration"
)

// Probe is one of a spec's requests as the engine sends it for some config:
// what a fake upstream matches to answer it (chadash spec test).
type Probe struct {
	Name     string
	Method   string
	Path     string     // under the instance's URL, templates filled
	Query    url.Values // the path's query and the request's, filled
	Optional bool
}

// Probes are the spec's requests, in order, for these config values
// (secrets included) and this instance, as Connect would fill them.
func (s *Spec) Probes(cfg integration.Config) []Probe {
	in := &instance{s: s, cfg: cfg, plain: map[string]any{}, secret: map[string]string{}}
	for k, v := range cfg.Values {
		if s.secrets[k] {
			in.secret[k], _ = v.(string)
		} else {
			in.plain[k] = v
		}
	}
	out := make([]Probe, 0, len(s.f.Requests))
	for _, r := range s.f.Requests {
		p := in.expand(r.Path)
		raw := ""
		if i := strings.IndexByte(p, '?'); i >= 0 {
			p, raw = p[:i], p[i+1:]
		}
		q, _ := url.ParseQuery(raw)
		for k, v := range r.Query {
			q.Set(k, in.expand(v))
		}
		out = append(out, Probe{Name: r.Name, Method: cmp(strings.ToUpper(r.Method), "GET"), Path: p, Query: q, Optional: r.Optional})
	}
	return out
}

// Summary is what a spec reads and what it can change, for the owner to
// review before installing it: each request and each action as written,
// templates unfilled.
type Summary struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Version  string          `json:"version"`
	Secrets  []string        `json:"secrets"`
	Requests []RequestInfo   `json:"requests"`
	Actions  []ActionSummary `json:"actions"`
}

// RequestInfo is one read.
type RequestInfo struct {
	Name     string `json:"name"`
	Method   string `json:"method"`
	Path     string `json:"path"`
	Optional bool   `json:"optional,omitempty"`
}

// ActionSummary is one action: its tier and the write it sends.
type ActionSummary struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Tier   string `json:"tier"`
	Method string `json:"method"`
	Path   string `json:"path"`
}

// Summarize describes the spec.
func (s *Spec) Summarize() Summary {
	out := Summary{ID: s.manifest.ID, Name: s.manifest.Name, Version: s.manifest.Version, Secrets: []string{}, Requests: []RequestInfo{}, Actions: []ActionSummary{}}
	for _, f := range s.f.Config {
		if s.secrets[f.Name] {
			out.Secrets = append(out.Secrets, f.Name)
		}
	}
	for _, r := range s.f.Requests {
		out.Requests = append(out.Requests, RequestInfo{Name: r.Name, Method: cmp(strings.ToUpper(r.Method), "GET"), Path: r.Path, Optional: r.Optional})
	}
	for _, a := range s.actions {
		out.Actions = append(out.Actions, ActionSummary{ID: a.tpl.ID, Title: a.tpl.Title, Tier: a.tier.String(), Method: strings.ToUpper(a.tpl.Request.Method), Path: a.tpl.Request.Path})
	}
	return out
}
