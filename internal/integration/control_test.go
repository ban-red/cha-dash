package integration

import (
	"encoding/json"
	"strings"
	"testing"
)

func controlManifest() Manifest {
	return Manifest{
		ID: "ctl", Name: "Ctl", Version: "0.1.0", Tier: Bronze,
		ConfigSchema: Object(URLProp(),
			Prop("token_id", String("Token ID")),
			Prop("token_secret", Secret("Token secret")),
			Prop("control_token_id", String("Control token ID").InGroup(GroupControl)),
			Prop("control_token_secret", Secret("Control token secret").InGroup(GroupControl)),
		).Require("url", "token_id", "token_secret"),
		Control: &Control{Fields: []ControlField{
			{Field: "control_token_id", Replaces: "token_id"},
			{Field: "control_token_secret", Replaces: "token_secret"},
		}, Missing: "Give {instance} a control token."},
	}
}

func TestControlConfig(t *testing.T) {
	m := controlManifest()
	if err := ValidateManifest(m); err != nil {
		t.Fatal(err)
	}
	full := Config{InstanceID: "i_1", Name: "pve", Values: map[string]any{"url": "https://pve.example", "token_id": "r@pve!r", "token_secret": "read",
		"control_token_id": "c@pve!c", "control_token_secret": "ctl"}}
	read := m.ReadConfig(full)
	if _, ok := read.Values["control_token_secret"]; ok || read.Values["token_secret"] != "read" || full.Values["control_token_secret"] != "ctl" {
		t.Fatalf("read config = %v (full %v)", read.Values, full.Values)
	}
	ctl, ok := m.ControlConfig(full)
	if !ok || ctl.Values["token_id"] != "c@pve!c" || ctl.Values["token_secret"] != "ctl" || ctl.Values["control_token_id"] != nil || ctl.InstanceID != "i_1" {
		t.Fatalf("control config = %v, %v", ctl.Values, ok)
	}
	if _, ok := m.ControlConfig(read); ok {
		t.Fatal("a config without the control group can act")
	}
	if got := m.ControlMissing("pve"); got != "Give pve a control token." {
		t.Fatalf("missing = %q", got)
	}
	half := map[string]any{"control_token_id": "c@pve!c"}
	if errs := m.CheckControl(half); len(errs) != 1 || errs[0].Path != "/control_token_secret" || !strings.Contains(errs[0].Message, "Control token ID") {
		t.Fatalf("half a group = %v", errs)
	}
	if errs := m.CheckControl(full.Values); errs != nil {
		t.Fatalf("whole group = %v", errs)
	}
	if errs := m.CheckControl(read.Values); errs != nil {
		t.Fatalf("no group = %v", errs)
	}

	// The schema says which fields are the control group.
	b, _ := json.Marshal(m.ConfigSchema)
	if !strings.Contains(string(b), `"control_token_secret":{"type":"string","title":"Control token secret","minLength":1,"maxLength":8192,"writeOnly":true,"x-chadash-group":"control"}`) {
		t.Fatalf("schema = %s", b)
	}

	// A mode: the same credential, when the mode is on.
	mode := Manifest{ID: "mode", Name: "Mode", Version: "1", Tier: Bronze,
		ConfigSchema: Object(Prop("api_key", Secret("Key")), Prop("control", Boolean("Control"))),
		Control:      &Control{Mode: "control"}}
	if err := ValidateManifest(mode); err != nil {
		t.Fatal(err)
	}
	on := Config{Values: map[string]any{"api_key": "k", "control": true}}
	if c, ok := mode.ControlConfig(on); !ok || c.Values["api_key"] != "k" {
		t.Fatal("mode on can't act")
	}
	if _, ok := mode.ControlConfig(Config{Values: map[string]any{"api_key": "k"}}); ok {
		t.Fatal("mode off can act")
	}
	if !strings.Contains(mode.ControlMissing("nas"), "nas is set up read-only") {
		t.Fatal(mode.ControlMissing("nas"))
	}
	// No Control: the one credential acts.
	plain := Manifest{ConfigSchema: Object(Prop("api_key", Secret("Key")))}
	if _, ok := plain.ControlConfig(Config{Values: map[string]any{"api_key": "k"}}); !ok {
		t.Fatal("without Control an Actor can't act")
	}
}

func TestControlManifestChecks(t *testing.T) {
	for name, mutate := range map[string]func(m *Manifest){
		"group without Control": func(m *Manifest) { m.Control = nil },
		"both fields and mode":  func(m *Manifest) { m.Control.Mode = "url" },
		"unknown field":         func(m *Manifest) { m.Control.Fields[0].Field = "nope" },
		"field not grouped":     func(m *Manifest) { m.ConfigSchema.Property("control_token_id").Group = "" },
		"required field":        func(m *Manifest) { m.ConfigSchema.Required = append(m.ConfigSchema.Required, "control_token_id") },
		"replaces a group field": func(m *Manifest) {
			m.Control.Fields[0].Replaces = "control_token_secret"
		},
		"secret replaces plain": func(m *Manifest) { m.Control.Fields[1].Replaces = "token_id" },
		"grouped but unlisted":  func(m *Manifest) { m.Control.Fields = m.Control.Fields[1:] },
		"mode not boolean": func(m *Manifest) {
			m.Control = &Control{Mode: "url"}
			m.ConfigSchema.Property("control_token_id").Group = ""
			m.ConfigSchema.Property("control_token_secret").Group = ""
		},
		"unknown group": func(m *Manifest) { m.ConfigSchema.Property("control_token_id").Group = "admin" },
		"nested group": func(m *Manifest) {
			m.ConfigSchema.Properties = append(m.ConfigSchema.Properties, Prop("nested", Object(Prop("x", String("X").InGroup(GroupControl)))))
		},
	} {
		m := controlManifest()
		mutate(&m)
		if err := ValidateManifest(m); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestControlRecipe(t *testing.T) {
	r := NewRegistry()
	read := Recipe{Title: "Read", Summary: "Reads.", Access: RecipeReadOnly, Steps: []RecipeStep{{Title: "Do it", Blocks: []RecipeBlock{{Kind: BlockText, Text: "Do it."}}}}}
	if err := r.AddRecipe("ctl", read); err != nil {
		t.Fatal(err)
	}
	ro := read
	if err := r.AddControlRecipe("ctl", ro); err == nil {
		t.Fatal("a read-only control recipe was accepted")
	}
	ctl := read
	ctl.Title, ctl.Access = "Control", RecipeScoped
	if err := r.AddControlRecipe("ctl", ctl); err != nil {
		t.Fatal(err)
	}
	if err := r.AddControlRecipe("ctl", ctl); err == nil {
		t.Fatal("a second control recipe was accepted")
	}
	got, ok := r.Recipe("ctl")
	if !ok || got.Control == nil || got.Control.Title != "Control" || got.Title != "Read" {
		t.Fatalf("recipe = %+v", got)
	}
	b, _ := json.Marshal(got)
	if !strings.Contains(string(b), `"control":{"title":"Control"`) {
		t.Fatalf("wire = %s", b)
	}
}

func TestExpectedWrites(t *testing.T) {
	grants := []Grant{{Name: "docker:update_any", Write: true}, {Name: "vms:read_any"}, {Name: "settings:update", Write: true}}
	if w := WriteGrantWarning(grants, "docker:update_any"); w == nil || strings.Contains(w.Message, "docker:update_any") || !strings.Contains(w.Message, "settings:update") {
		t.Fatalf("warning = %+v", w)
	}
	if w := WriteGrantWarning(grants[:2], "docker:update_any"); w != nil {
		t.Fatalf("an expected write warned: %+v", w)
	}
	// One credential that always acts.
	app := Manifest{ID: "app", Name: "App", Version: "1", Tier: Bronze, ConfigSchema: Object(Prop("api_key", Secret("Key"))),
		Control: &Control{Grants: []string{"queue:write"}}}
	if err := ValidateManifest(app); err != nil {
		t.Fatal(err)
	}
	if got := app.ExpectedWrites(Config{Values: map[string]any{"api_key": "k"}}); len(got) != 1 {
		t.Fatalf("expected = %v", got)
	}
	// A separate control credential is never what Test checks.
	m := controlManifest()
	m.Control.Grants = []string{"VM.PowerMgmt"}
	if got := m.ExpectedWrites(Config{Values: map[string]any{"control_token_id": "c", "control_token_secret": "s"}}); got != nil {
		t.Fatalf("expected = %v", got)
	}
	empty := Manifest{ID: "e", Name: "E", Version: "1", Tier: Bronze, ConfigSchema: Object(Prop("api_key", Secret("Key"))), Control: &Control{}}
	if err := ValidateManifest(empty); err == nil {
		t.Fatal("an empty Control was accepted")
	}
}
