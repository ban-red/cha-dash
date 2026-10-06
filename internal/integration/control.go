package integration

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// GroupControl is the form group of an instance's control credential: the
// optional fields that let Cha Dash act, kept apart from the credential it
// reads with (docs/plan/08-v0.2.md, "Control credentials").
const GroupControl = "control"

// HubResource is the hub's own resource in the Home Graph. An integration
// that can tell where the hub runs (the Proxmox guest it lives in) relates
// it there with runs_on, so the blast radius and the self-aware T4 see it.
const HubResource = "chadash:hub"

// Control says how an instance's actions get the credential they act with.
// Without it, an Actor's actions use the instance's one credential (an *arr
// API key can already do everything).
//
// There are two shapes:
//
//   - Fields: a second credential. Each control field is a top-level
//     property marked Group: GroupControl (Secret(…).InGroup(GroupControl)
//     for a secret), optional, standing in for a read field when the
//     instance acts. Proxmox maps control_token_id → token_id and
//     control_token_secret → token_secret. The group is all or nothing, and
//     an instance with it empty has no control credential: its actions
//     answer 409 unavailable with Missing.
//   - Mode: one credential, with the mode chosen in config (Unraid, decision
//     6). Mode names a boolean property; true lets the instance act with
//     the credential it reads with, false (or absent) keeps it read-only.
//   - Neither, with Grants: one credential that always acts (an app's
//     full-access key), declared only to name its expected write grants.
//
// Grants are the write grants (as Test reports them) the acting credential
// needs for the actions. When the credential Test checks is the one that
// acts (a mode that's on, or no control fields), they don't raise the
// "write grant" warning: Cha Dash uses them. A separate control credential
// is never what Test checks, so its grants never reach that warning.
//
// The runtime connects the instance Sync runs on with ReadConfig (no
// control fields), and the instance Execute runs on with ControlConfig.
type Control struct {
	Fields []ControlField `json:"fields,omitempty"`
	Mode   string         `json:"mode,omitempty"`
	Grants []string       `json:"grants,omitempty"`
	// Missing is the sentence an action answers without the control
	// credential; "{instance}" is replaced by the instance's name. Empty
	// gives a generic one.
	Missing string `json:"missing,omitempty"`
}

// ControlField is one field of the control credential and the read field it
// replaces when the instance acts.
type ControlField struct {
	Field    string `json:"field"`
	Replaces string `json:"replaces"`
}

// ControlFields lists the control group's fields, in Control order.
func (m Manifest) ControlFields() []string {
	if m.Control == nil {
		return nil
	}
	out := make([]string, len(m.Control.Fields))
	for i, f := range m.Control.Fields {
		out[i] = f.Field
	}
	return out
}

// ReadConfig is cfg without the control group: what Sync, Test and the
// action hooks other than Execute see.
func (m Manifest) ReadConfig(cfg Config) Config {
	fields := m.ControlFields()
	if len(fields) == 0 {
		return cfg
	}
	out := cfg
	out.Values = maps.Clone(cfg.Values)
	for _, f := range fields {
		delete(out.Values, f)
	}
	return out
}

// ControlConfig is the config the instance that executes actions gets, and
// whether the instance may act at all: with Fields, the read fields
// replaced by the control credential (false while the group is empty);
// with Mode, cfg as it is when the mode is on; without Control, cfg.
func (m Manifest) ControlConfig(cfg Config) (Config, bool) {
	c := m.Control
	switch {
	case c == nil:
		return cfg, true
	case c.Mode != "":
		on, _ := cfg.Values[c.Mode].(bool)
		return cfg, on
	case len(c.Fields) == 0:
		return cfg, true
	}
	out := cfg
	out.Values = maps.Clone(cfg.Values)
	for _, f := range c.Fields {
		v, ok := cfg.Values[f.Field]
		if !ok || v == nil || v == "" {
			return Config{}, false
		}
		out.Values[f.Replaces] = v
		delete(out.Values, f.Field)
	}
	return out, true
}

// ExpectedWrites are the write grants a config's tested credential is
// expected to hold for its actions (Control.Grants), or nil when the
// tested credential doesn't act: read-only mode, or a separate control
// credential.
func (m Manifest) ExpectedWrites(cfg Config) []string {
	c := m.Control
	if c == nil || len(c.Fields) > 0 {
		return nil
	}
	if _, ok := m.ControlConfig(cfg); !ok {
		return nil
	}
	return c.Grants
}

// ControlMissing is the sentence an instance's actions answer without the
// control credential.
func (m Manifest) ControlMissing(instance string) string {
	msg := ""
	if m.Control != nil {
		msg = m.Control.Missing
	}
	if msg == "" {
		switch {
		case m.Control != nil && m.Control.Mode != "":
			msg = "{instance} is set up read-only. Let Cha Dash control it in the instance's settings."
		default:
			msg = "Add a control credential to {instance} to let Cha Dash do this. Its recipe shows how."
		}
	}
	return strings.ReplaceAll(msg, "{instance}", instance)
}

// CheckControl finds a control group filled in halfway: every field or
// none. values is a config with its secrets resolved.
func (m Manifest) CheckControl(values map[string]any) []FieldError {
	if m.Control == nil || len(m.Control.Fields) == 0 {
		return nil
	}
	var set, unset []string
	for _, f := range m.Control.Fields {
		if v, ok := values[f.Field]; ok && v != nil && v != "" {
			set = append(set, f.Field)
		} else {
			unset = append(unset, f.Field)
		}
	}
	if len(set) == 0 || len(unset) == 0 {
		return nil
	}
	var out []FieldError
	for _, f := range unset {
		out = append(out, FieldError{Path: "/" + f, Message: fmt.Sprintf("is needed with %s: the control credential is all or nothing", m.title(set[0]))})
	}
	return out
}

func (m Manifest) title(field string) string {
	if s := m.ConfigSchema.Property(field); s != nil && s.Title != "" {
		return s.Title
	}
	return field
}

// checkControl validates Manifest.Control against the schema.
func checkControl(m Manifest) error {
	var grouped []string
	for _, p := range m.ConfigSchema.Properties {
		if p.Schema.Group == GroupControl {
			grouped = append(grouped, p.Name)
		}
	}
	c := m.Control
	if c == nil {
		if len(grouped) > 0 {
			return fmt.Errorf("properties in the control group (%s) need Manifest.Control", strings.Join(grouped, ", "))
		}
		return nil
	}
	if len(c.Fields) > 0 && c.Mode != "" {
		return fmt.Errorf("control: set Fields or Mode, not both")
	}
	if len(c.Fields) == 0 && c.Mode == "" {
		if len(c.Grants) == 0 {
			return fmt.Errorf("control: set Fields, Mode or Grants")
		}
		if len(grouped) > 0 {
			return fmt.Errorf("control: %s are in the control group but Control has no Fields", strings.Join(grouped, ", "))
		}
		return nil
	}
	if c.Mode != "" {
		s := m.ConfigSchema.Property(c.Mode)
		if s == nil || s.Type != "boolean" {
			return fmt.Errorf("control: mode %q must be a boolean property", c.Mode)
		}
		if len(grouped) > 0 {
			return fmt.Errorf("control: a mode has no control fields, but %s are in the group", strings.Join(grouped, ", "))
		}
		return nil
	}
	seen := map[string]bool{}
	for _, f := range c.Fields {
		s := m.ConfigSchema.Property(f.Field)
		switch {
		case s == nil:
			return fmt.Errorf("control: field %q isn't a property", f.Field)
		case s.Group != GroupControl:
			return fmt.Errorf("control: field %q needs \"x-chadash-group\": %q", f.Field, GroupControl)
		case slices.Contains(m.ConfigSchema.Required, f.Field):
			return fmt.Errorf("control: field %q can't be required", f.Field)
		case seen[f.Field]:
			return fmt.Errorf("control: field %q is listed twice", f.Field)
		}
		seen[f.Field] = true
		r := m.ConfigSchema.Property(f.Replaces)
		switch {
		case r == nil || r.Group != "":
			return fmt.Errorf("control: %q replaces %q, which isn't a read field", f.Field, f.Replaces)
		case r.WriteOnly != s.WriteOnly || r.Type != s.Type:
			return fmt.Errorf("control: %q and the field it replaces, %q, must be the same kind", f.Field, f.Replaces)
		}
	}
	for _, g := range grouped {
		if !seen[g] {
			return fmt.Errorf("control: %q is in the control group but not in Control.Fields", g)
		}
	}
	return nil
}
