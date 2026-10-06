package export

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/correlate"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/security"
)

// The security settings and the correlation decisions: two things a Home
// file carries since v0.2 beside boards, instances and import records.
// Everything this package knows about them is in this file.
//
// In a Home file:
//
//	security:
//	  posture: standard              # relaxed, standard or strict (internal/security)
//	  step_up: passkey_or_password   # or passkey_only
//	correlation:                     # the owner's answers to "these look like the same thing"
//	  - {from: docker:i_d/container/sonarr, rel: same_as, to: unraid:i_u/container/sonarr, verdict: confirm, rule: name}
//
// A file without a security key leaves the hub's settings alone; one
// without a correlation key leaves its decisions alone. On import:
//
//   - security is one "security" change with field-level changes (/posture,
//     /step_up). Only the owner may import it, and a different step-up rule
//     needs a step-up grant, as in Settings → Security
//     (authz.ChangeSecuritySettings, through WithSecurityGate). Refused, it's
//     reported with skipped and a security_skipped warning, and the rest of
//     the file still imports.
//   - each decision is a "correlation" change matched by its pair (same_as
//     pairs in either order), created or updated (/verdict, /rule); the hub's
//     decisions the file doesn't have are stale, never deleted. Decisions
//     reference resources by ID, and instance IDs survive an import, so they
//     need no remapping. A decision goes when either of its instances does,
//     as before.

// Security is the hub's security settings.
type Security struct {
	Posture string
	StepUp  string
}

// Decision is one correlation decision: a pair, the relation and the
// owner's verdict (confirm or reject), and the rule that suggested it.
type Decision struct {
	From, Rel, To string
	Verdict       string
	Rule          string
}

func (d Decision) key() string { return d.From + " " + d.Rel + " " + d.To }

func (d Decision) words() string {
	return d.From + " " + strings.ReplaceAll(d.Rel, "_", " ") + " " + d.To
}

// normalized orders a same_as pair the way the hub stores it.
func (d Decision) normalized() Decision {
	f, t := correlate.StoredPair(graph.RID(d.From), graph.RelType(d.Rel), graph.RID(d.To))
	d.From, d.To = string(f), string(t)
	return d
}

func validRel(rel string) bool {
	return slices.Contains(correlate.DecisionTypes(), graph.RelType(rel))
}

// readSecurity reads the settings in force: a missing or unknown row reads
// as its default, as internal/security does.
func readSecurity(ctx context.Context, q querier) (Security, error) {
	sec := Security{Posture: string(security.Default), StepUp: string(auth.DefaultStepUpMode)}
	for _, f := range []struct {
		key   string
		dst   *string
		valid func(string) bool
	}{
		{security.SettingsKey, &sec.Posture, func(s string) bool { _, ok := security.Parse(s); return ok }},
		{security.StepUpKey, &sec.StepUp, func(s string) bool { _, ok := auth.ParseStepUpMode(s); return ok }},
	} {
		var raw string
		err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, f.key).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return sec, err
		}
		var v string
		if json.Unmarshal([]byte(raw), &v) == nil && f.valid(v) {
			*f.dst = v
		}
	}
	return sec, nil
}

// readDecisions lists the hub's decisions by pair.
func readDecisions(ctx context.Context, q querier) ([]Decision, error) {
	rows, err := q.QueryContext(ctx, `SELECT from_id, rel, to_id, verdict, rule FROM correlation_decisions ORDER BY from_id, rel, to_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Decision{}
	for rows.Next() {
		var d Decision
		if err := rows.Scan(&d.From, &d.Rel, &d.To, &d.Verdict, &d.Rule); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// securityGateKey carries who may change the security settings.
type securityGateKey struct{}

// WithSecurityGate makes Plan and Apply under ctx ask gate before changing
// the security settings; stepUp says whether the step-up rule would change.
// An error skips the security change (with its sentence) and imports the
// rest. Without a gate (the CLI, which reads the data directory directly,
// so it already has the owner's power) the change is allowed.
func WithSecurityGate(ctx context.Context, gate func(stepUp bool) error) context.Context {
	return context.WithValue(ctx, securityGateKey{}, gate)
}

func securityGate(ctx context.Context) func(bool) error {
	g, _ := ctx.Value(securityGateKey{}).(func(bool) error)
	return g
}

func (s Security) view() map[string]any {
	return map[string]any{"posture": s.Posture, "step_up": s.StepUp}
}

// compareSecurity plans the security change.
func (s *Service) compareSecurity(ctx context.Context, q querier, d *Document, w *work) error {
	if d.Security == nil {
		return nil
	}
	p := &w.plan
	cur, err := readSecurity(ctx, q)
	if err != nil {
		return fmt.Errorf("export: security settings: %w", err)
	}
	want := *d.Security
	c := Change{}
	c.Kind, c.ID, c.Name = "security", "security", "Security"
	if cur == want {
		c.Action = actionUnchanged
		p.Changes = append(p.Changes, c)
		return nil
	}
	c.Action = actionUpdate
	c.diff("", cur.view(), want.view())
	if gate := securityGate(ctx); gate != nil {
		if err := gate(cur.StepUp != want.StepUp); err != nil {
			c.Action, c.Skipped = actionUnchanged, err.Error()
			p.Warnings = append(p.Warnings, Warning{Code: "security_skipped", Kind: "security", ID: "security", Message: err.Error()})
			p.Changes = append(p.Changes, c)
			return nil
		}
	}
	w.security = &want
	p.Changes = append(p.Changes, c)
	return nil
}

// compareDecisions plans the correlation decisions.
func (s *Service) compareDecisions(ctx context.Context, q querier, d *Document, w *work) error {
	if d.Correlation == nil {
		return nil
	}
	p := &w.plan
	have, err := readDecisions(ctx, q)
	if err != nil {
		return fmt.Errorf("export: correlation decisions: %w", err)
	}
	// Pairs match in stored order, whichever way round either side wrote a
	// same_as pair; an update keeps the row the hub has.
	cur := map[string]Decision{}
	for _, h := range have {
		cur[h.normalized().key()] = h
	}
	seen := map[string]bool{}
	for _, fd := range d.Correlation {
		fd = fd.normalized()
		k := fd.key()
		seen[k] = true
		c := Change{}
		c.Kind, c.ID, c.Name = "correlation", k, fd.words()
		h, ok := cur[k]
		switch {
		case !ok:
			c.Action, c.Detail = actionCreate, fd.Verdict
		case h.Verdict == fd.Verdict && h.Rule == fd.Rule:
			c.Action = actionUnchanged
		default:
			c.Action = actionUpdate
			c.diff("", map[string]any{"verdict": h.Verdict, "rule": h.Rule}, map[string]any{"verdict": fd.Verdict, "rule": fd.Rule})
			fd.From, fd.To = h.From, h.To
		}
		if c.Action != actionUnchanged {
			w.decisions = append(w.decisions, fd)
		}
		p.Changes = append(p.Changes, c)
	}
	for _, h := range have {
		if n := h.normalized(); !seen[n.key()] {
			c := Change{}
			c.Kind, c.ID, c.Name, c.Action = "correlation", n.key(), n.words(), actionStale
			p.Changes = append(p.Changes, c)
		}
	}
	return nil
}

// instanceOfRID is the instance a resource ID names: "<integration>:<instance>/…".
func instanceOfRID(id string) string {
	_, rest, ok := strings.Cut(id, ":")
	if !ok {
		return ""
	}
	inst, _, _ := strings.Cut(rest, "/")
	return inst
}

// writeExtras writes the security settings and the decisions inside the
// import's transaction.
func writeExtras(ctx context.Context, tx *sql.Tx, w *work, userID string, now int64) error {
	if w.security != nil {
		for key, v := range map[string]string{security.SettingsKey: w.security.Posture, security.StepUpKey: w.security.StepUp} {
			b, _ := json.Marshal(v)
			if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
				ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, string(b), now); err != nil {
				return fmt.Errorf("export: setting %s: %w", key, err)
			}
		}
	}
	for _, d := range w.decisions {
		_, err := tx.ExecContext(ctx, `INSERT INTO correlation_decisions
				(id, from_id, rel, to_id, verdict, rule, from_instance, to_instance, created_by, created_at)
			VALUES (?, ?, ?, ?, ?, ?,
				(SELECT id FROM integration_instances WHERE id = ?), (SELECT id FROM integration_instances WHERE id = ?),
				(SELECT id FROM users WHERE id = ?), ?)
			ON CONFLICT (from_id, rel, to_id) DO UPDATE SET
				verdict = excluded.verdict, rule = excluded.rule, from_instance = excluded.from_instance,
				to_instance = excluded.to_instance, created_by = excluded.created_by, created_at = excluded.created_at`,
			newDecisionID(), d.From, d.Rel, d.To, d.Verdict, d.Rule, instanceOfRID(d.From), instanceOfRID(d.To), userID, now)
		if err != nil {
			return fmt.Errorf("export: correlation decision %s: %w", d.key(), err)
		}
	}
	return nil
}

func newDecisionID() string { return "cd_" + strings.ToLower(rand.Text()) }

// security reads the security key: both settings, each a known value.
func (p *parser) security(n *yaml.Node) *Security {
	s := &Security{}
	var sawPosture, sawStepUp bool
	p.fields(n, "security", []string{"posture", "step_up"}, func(key string, v *yaml.Node) {
		kp := "security." + key
		val, ok := p.scalar(v, kp)
		if !ok {
			return
		}
		switch key {
		case "posture":
			sawPosture = true
			if _, known := security.Parse(val); !known {
				p.fail(v, kp, "must be relaxed, standard or strict")
				return
			}
			s.Posture = val
		case "step_up":
			sawStepUp = true
			if _, known := auth.ParseStepUpMode(val); !known {
				p.fail(v, kp, "must be passkey_or_password or passkey_only")
				return
			}
			s.StepUp = val
		}
	})
	if p.err == nil && (!sawPosture || !sawStepUp) {
		p.fail(n, "security", "needs posture and step_up")
	}
	return s
}

// correlation reads the correlation key: a list of decisions, each pair at
// most once (same_as pairs in either order count as one).
func (p *parser) correlation(n *yaml.Node) []Decision {
	out := []Decision{}
	if n.Kind != yaml.SequenceNode {
		p.fail(n, "correlation", "must be a list")
		return out
	}
	seen := map[string]bool{}
	for i, dn := range n.Content {
		path := fmt.Sprintf("correlation[%d]", i)
		var d Decision
		have := map[string]bool{}
		p.fields(dn, path, []string{"from", "rel", "to", "verdict", "rule"}, func(key string, v *yaml.Node) {
			kp := path + "." + key
			have[key] = true
			switch key {
			case "from":
				d.From = p.text(v, kp, 512)
			case "to":
				d.To = p.text(v, kp, 512)
			case "rel":
				if d.Rel, _ = p.scalar(v, kp); p.err == nil && !validRel(d.Rel) {
					p.fail(v, kp, "must be same_as, runs_on, routes_to, stores_on, depends_on or monitors")
				}
			case "verdict":
				if d.Verdict, _ = p.scalar(v, kp); p.err == nil && d.Verdict != "confirm" && d.Verdict != "reject" {
					p.fail(v, kp, "must be confirm or reject")
				}
			case "rule":
				d.Rule, _ = p.scalar(v, kp)
				if p.err == nil && (len(d.Rule) > 64 || strings.ContainsFunc(d.Rule, func(r rune) bool { return r < 0x20 || r == 0x7f })) {
					p.fail(v, kp, "must be at most 64 characters on one line")
				}
			}
		})
		if p.err != nil {
			return out
		}
		switch {
		case !have["from"] || !have["rel"] || !have["to"] || !have["verdict"]:
			p.fail(dn, path, "needs from, rel, to and verdict")
		case d.From == d.To:
			p.fail(dn, path, "relates a resource to itself")
		case seen[d.normalized().key()]:
			p.fail(dn, path, "decides a pair another entry in this file already decides")
		}
		if p.err != nil {
			return out
		}
		seen[d.normalized().key()] = true
		out = append(out, d)
	}
	return out
}
