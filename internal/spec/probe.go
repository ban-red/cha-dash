package spec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"net/url"
	"slices"
	"strings"

	"dash.cha.sh/internal/integration"
)

// Probe is one of a spec's requests as the engine sends it for some config:
// what a fake upstream matches to answer it (chadash spec test).
type Probe struct {
	Name   string
	Method string
	Path   string     // under the instance's URL, templates filled
	Query  url.Values // the path's query and the request's, filled
	// Body is a POST's body, templates filled; "" for a GET. Requests that
	// share a path (Komodo's POST /read) differ by it.
	Body     string
	Optional bool
}

// Probes are the spec's requests, in order, for these config values
// (secrets included) and this instance, as Connect would fill them.
func (s *Spec) Probes(cfg integration.Config) []Probe {
	in := s.newInstance(cfg)
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
		body := ""
		if r.Body != "" {
			body = in.expand(r.Body)
		}
		out = append(out, Probe{Name: r.Name, Method: cmp(strings.ToUpper(r.Method), "GET"), Path: p, Query: q, Body: body, Optional: r.Optional})
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
	// ActionsDigest covers every key of the actions and their targets: two
	// specs with the same digest can change the same things the same way.
	ActionsDigest string `json:"actions_digest"`
}

// RequestInfo is one read. A POST can change things whatever it's called:
// an installed spec's run only with its actions on.
type RequestInfo struct {
	Name     string `json:"name"`
	Method   string `json:"method"`
	Path     string `json:"path"`
	Optional bool   `json:"optional,omitempty"`
	// Secrets are the secret fields it sends (in its query, its headers,
	// its body, or the source's headers), sorted.
	Secrets []string `json:"secrets,omitempty"`
	// Body is a POST's body as written, templates unfilled.
	Body string `json:"body,omitempty"`
}

// ActionSummary is one action: its tier and the write it sends.
type ActionSummary struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Tier   string `json:"tier"`
	Method string `json:"method"`
	Path   string `json:"path"`
	// Body is the keys of the JSON body it sends, sorted.
	Body []string `json:"body,omitempty"`
	// Household marks an action the Household role may run.
	Household bool `json:"household,omitempty"`
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
		var texts []string
		texts = append(texts, slices.Collect(maps.Values(r.Query))...)
		texts = append(texts, slices.Collect(maps.Values(r.Headers))...)
		texts = append(texts, slices.Collect(maps.Values(s.f.Source.Headers))...)
		texts = append(texts, r.Body)
		out.Requests = append(out.Requests, RequestInfo{Name: r.Name, Method: cmp(strings.ToUpper(r.Method), "GET"), Path: r.Path, Optional: r.Optional,
			Secrets: secretsIn(texts), Body: r.Body})
	}
	for _, a := range s.actions {
		out.Actions = append(out.Actions, ActionSummary{ID: a.tpl.ID, Title: a.tpl.Title, Tier: a.tier.String(), Method: strings.ToUpper(a.tpl.Request.Method), Path: a.tpl.Request.Path,
			Body: slices.Sorted(maps.Keys(a.tpl.Request.Body)), Household: a.tpl.Household})
	}
	out.ActionsDigest = s.actionsDigest()
	return out
}

// actionsDigest hashes everything that decides what the spec can change,
// as parsed: the actions and targets, every key included (bodies, params,
// preconditions, verify, undo, household, the targets' patterns and
// reads); the POST requests, which run only with the actions on; and what
// actions send or read besides (the source's headers and timeout, the
// config fields). JSON sorts map keys, so it's stable.
func (s *Spec) actionsDigest() string {
	var posts []request
	for _, r := range s.f.Requests {
		if strings.EqualFold(r.Method, "POST") {
			posts = append(posts, r)
		}
	}
	b, _ := json.Marshal(struct {
		Actions []actionTpl
		Targets targets
		Posts   []request
		Source  source
		Config  fields
	}{s.f.Actions, s.f.Targets, posts, s.f.Source, s.f.Config})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// secretsIn names the secret fields the templates in texts fill, sorted.
func secretsIn(texts []string) []string {
	var out []string
	for _, t := range texts {
		for _, m := range templateRE.FindAllStringSubmatch(t, -1) {
			if m[1] == "secret" && !slices.Contains(out, m[2]) {
				out = append(out, m[2])
			}
		}
	}
	slices.Sort(out)
	return out
}
