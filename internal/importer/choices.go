package importer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Choices are what the person decided in the Review sheet: what to leave
// out, where the tiles go, which connections to reuse, and credentials typed
// for fields the source didn't hold. The zero value imports everything, onto
// the source's own new board, as before.
type Choices struct {
	// Skip lists plan objects to leave out: "instance:<id>" or
	// "tile:<item id>". Leaving out a connection leaves out the tiles and
	// links that need it; leaving out a tile drops its check target when no
	// other tile watches it. Unknown IDs are ignored (the plan hash catches
	// a plan that moved).
	Skip []string `json:"skip,omitempty"`
	// Target is where tiles go: "" or "new" (the plan's own board), "none"
	// (connections only, no board), or "board:<id>" (added to an existing
	// board, after its own items).
	Target string `json:"target,omitempty"`
	// UseExisting maps a planned instance to one the hub already has, of the
	// same integration: the planned one isn't created and its tiles bind to
	// the existing one.
	UseExisting map[string]string `json:"use_existing,omitempty"`
	// Secrets are values typed for a planned instance's missing secret
	// fields, by instance ID and field.
	Secrets map[string]map[string]Secret `json:"secrets,omitempty"`
}

// Targets.
const (
	TargetNew  = "new"
	TargetNone = "none"
)

// ErrInvalidChoices is a Choices that can't apply to the plan: an unknown
// target form, a field that isn't missing, a reuse across integrations.
var ErrInvalidChoices = errors.New("importer: invalid choices")

// ErrPlanChanged is Apply refusing a plan that no longer matches the one
// reviewed: the hub's state or the files changed in between.
var ErrPlanChanged = errors.New("importer: the plan changed since it was reviewed")

// TargetBoard returns the board ID of a "board:<id>" target.
func (c Choices) TargetBoard() (string, bool) {
	id, ok := strings.CutPrefix(c.Target, "board:")
	return id, ok && id != ""
}

func (c Choices) validate() error {
	switch t := c.Target; {
	case t == "", t == TargetNew, t == TargetNone:
	default:
		if _, ok := c.TargetBoard(); !ok {
			return fmt.Errorf("%w: target %q isn't new, none or board:<id>", ErrInvalidChoices, t)
		}
	}
	for _, s := range c.Skip {
		if !strings.HasPrefix(s, "instance:") && !strings.HasPrefix(s, "tile:") {
			return fmt.Errorf("%w: skip %q isn't instance:<id> or tile:<id>", ErrInvalidChoices, s)
		}
	}
	return nil
}

// UnmarshalJSON reads a secret typed in the Review sheet. Marshalling still
// redacts it.
func (s *Secret) UnmarshalJSON(b []byte) error {
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	s.v = v
	return nil
}

// fillSecrets moves typed values into the plan as vault secrets, the way a
// parser does for values in the files.
func fillSecrets(res *Result, secrets Secrets, typed map[string]map[string]Secret) error {
	p := &res.Plan
	for id, fields := range typed {
		i := slices.IndexFunc(p.Instances, func(in Instance) bool { return in.ID == id })
		if i < 0 {
			continue // left out, or gone: the hash catches the latter
		}
		in := &p.Instances[i]
		for f, v := range fields {
			if v.Reveal() == "" {
				continue
			}
			if !slices.Contains(in.MissingSecrets, f) {
				return fmt.Errorf("%w: %s has no missing field %q", ErrInvalidChoices, in.Name, f)
			}
			name := "instance/" + in.ID + "/" + f
			secrets[name] = v
			if in.Config == nil {
				in.Config = map[string]any{}
			}
			in.Config[f] = SecretRefValue(name)
			in.SecretFields = append(in.SecretFields, f)
			in.MissingSecrets = slices.DeleteFunc(in.MissingSecrets, func(m string) bool { return m == f })
			p.Secrets = append(p.Secrets, SecretRef{Name: name, Instance: in.ID, Field: f})
		}
	}
	// A missing-secret hint goes once its instance has none missing.
	res.Hints = slices.DeleteFunc(res.Hints, func(h Hint) bool {
		if h.Code != "missing_secret" {
			return false
		}
		for _, s := range h.Subjects {
			if i := slices.IndexFunc(p.Instances, func(in Instance) bool { return in.ID == s.Instance }); i >= 0 && len(p.Instances[i].MissingSecrets) > 0 {
				return false
			}
		}
		return true
	})
	return nil
}

// reuse binds the planned instances in m to existing ones and drops them
// from the plan.
func reuse(res *Result, secrets Secrets, m map[string]string) {
	p := &res.Plan
	for planned, existing := range m {
		for i := range p.Tiles {
			t := &p.Tiles[i]
			if t.Instance == planned {
				t.Instance = existing
			}
			for j := range t.Sources {
				if t.Sources[j].Instance == planned {
					t.Sources[j].Instance = existing
				}
			}
		}
		for i := range p.Links {
			if p.Links[i].Instance == planned {
				p.Links[i].Instance = existing
			}
			if p.Links[i].Docker == planned {
				p.Links[i].Docker = existing
			}
		}
		dropInstance(p, secrets, planned)
	}
}

func dropInstance(p *Plan, secrets Secrets, id string) {
	p.Instances = slices.DeleteFunc(p.Instances, func(in Instance) bool { return in.ID == id })
	p.Secrets = slices.DeleteFunc(p.Secrets, func(r SecretRef) bool {
		if r.Instance == id {
			delete(secrets, r.Name)
			return true
		}
		return false
	})
}

// skip leaves out what s names, and what depends on it.
func skip(res *Result, secrets Secrets, s []string) {
	p := &res.Plan
	for _, id := range s {
		inst, ok := strings.CutPrefix(id, "instance:")
		if !ok {
			continue
		}
		dropInstance(p, secrets, inst)
		p.Links = slices.DeleteFunc(p.Links, func(l Link) bool { return l.Instance == inst || l.Docker == inst })
		var gone []string
		for i := range p.Tiles {
			t := &p.Tiles[i]
			t.Sources = slices.DeleteFunc(t.Sources, func(src Source) bool { return src.Instance == inst })
			if t.Instance == inst || (t.Instance == "" && t.Check == "" && len(t.Sources) == 0 && t.Widget == "calendar") {
				gone = append(gone, t.ItemID)
			}
		}
		for _, item := range gone {
			dropTile(p, item)
		}
	}
	for _, id := range s {
		if item, ok := strings.CutPrefix(id, "tile:"); ok {
			dropTile(p, item)
		}
	}
}

// dropTile removes a tile from the plan and its layout, and its check target
// when no other tile watches it.
func dropTile(p *Plan, item string) {
	i := slices.IndexFunc(p.Tiles, func(t Tile) bool { return t.ItemID == item })
	if i < 0 {
		return
	}
	t := p.Tiles[i]
	p.Tiles = slices.Delete(p.Tiles, i, i+1)
	for s := range p.Board.Layout.Sections {
		sec := &p.Board.Layout.Sections[s]
		sec.Items = slices.DeleteFunc(sec.Items, func(it LayoutItem) bool { return it.ID == item })
	}
	for l := range p.Links {
		if p.Links[l].Tile == item {
			p.Links[l].Tile = ""
		}
	}
	p.Links = slices.DeleteFunc(p.Links, func(l Link) bool { return l.Tile == "" && l.Instance == "" })
	if t.Check == "" || slices.ContainsFunc(p.Tiles, func(o Tile) bool { return o.Check == t.Check }) {
		return
	}
	for k := range p.Instances {
		in := &p.Instances[k]
		if in.Integration != "checks" || in.ID != t.Instance {
			continue
		}
		targets, _ := in.Config["targets"].([]map[string]any)
		targets = slices.DeleteFunc(slices.Clone(targets), func(c map[string]any) bool { return c["name"] == t.Check })
		in.Config["targets"] = targets
		if len(targets) == 0 {
			p.Instances = slices.Delete(p.Instances, k, k+1)
		}
		return
	}
}

// noBoard turns the plan into connections only.
func noBoard(p *Plan) {
	p.Tiles = nil
	p.Board = Board{}
	for i := range p.Links {
		p.Links[i].Tile = ""
	}
	p.Links = slices.DeleteFunc(p.Links, func(l Link) bool { return l.Instance == "" })
}

// recount brings the summary's counts in line with a plan the choices
// changed. Notes and the source's own counts (hints, unmapped, ignored)
// stay.
func recount(res *Result) {
	p := &res.Plan
	s := &p.Summary
	s.Sections = len(p.Board.Layout.Sections)
	s.Tiles = len(p.Tiles)
	s.Instances = len(p.Instances)
	s.Links = len(p.Links)
	s.Checks = 0
	s.Integrations = map[string]int{}
	for _, in := range p.Instances {
		s.Integrations[in.Integration]++
		if in.Integration == "checks" {
			if ts, ok := in.Config["targets"].([]map[string]any); ok {
				s.Checks += len(ts)
			} else if ts, ok := in.Config["targets"].([]any); ok {
				s.Checks += len(ts)
			}
		}
	}
	p.SecretsMoved = len(p.Secrets)
	s.SecretsMoved = p.SecretsMoved
	s.Hints = len(res.Hints)
	s.InboxHints = 0
	for _, h := range res.Hints {
		if h.Inbox {
			s.InboxHints++
		}
	}
}

// Hash is the reviewed plan's fingerprint: a SHA-256 over the result as JSON
// (which never holds a secret value), changes included, so a hub that
// changed since the dry run gives another hash.
func Hash(res *Result) string {
	cp := *res
	cp.PlanHash = ""
	b, err := json.Marshal(cp)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
