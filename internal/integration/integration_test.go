package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"dash.cha.sh/sim"
)

type fake struct{ m Manifest }

func (f fake) Manifest() Manifest { return f.m }
func (fake) Connect(context.Context, Config, Deps) (Instance, error) {
	return nil, errors.New("not used")
}

func manifest(id string) Manifest {
	return Manifest{
		ID: id, Name: "Fake", Icon: "fake", Version: "0.1.0", Tier: Bronze,
		Capabilities: []Capability{"primitives.stat"},
		ConfigSchema: Object(URLProp(), TLSProp(), Prop("api_key", Secret("API key"))).Require("url", "api_key"),
	}
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(fake{manifest("sonarr")}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(fake{manifest("radarr")}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(fake{manifest("sonarr")}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate: %v", err)
	}
	_, m, ok := r.Get("sonarr")
	if !ok || m.Engine != EngineGo || m.Poll.Interval != DefaultInterval || m.Poll.Idle != DefaultIdle {
		t.Fatalf("registered manifest = %+v", m)
	}
	if _, _, ok := r.Get("plex"); ok {
		t.Fatal("plex isn't registered")
	}
	var ids []string
	for _, m := range r.Manifests() {
		ids = append(ids, m.ID)
	}
	if !reflect.DeepEqual(ids, []string{"radarr", "sonarr"}) {
		t.Fatalf("manifests = %v", ids)
	}
	if got := m.SecretFields(); !reflect.DeepEqual(got, []string{"api_key"}) {
		t.Fatalf("secret fields = %v", got)
	}

	// Register (the init-time form) panics on a bad manifest.
	defer func() {
		if recover() == nil {
			t.Fatal("Register should panic on an invalid manifest")
		}
	}()
	Register(fake{Manifest{ID: "Bad ID"}})
}

func TestValidateManifest(t *testing.T) {
	bad := map[string]func(*Manifest){
		"id":         func(m *Manifest) { m.ID = "Sonarr" },
		"name":       func(m *Manifest) { m.Name = " " },
		"version":    func(m *Manifest) { m.Version = "" },
		"tier":       func(m *Manifest) { m.Tier = "platinum" },
		"engine":     func(m *Manifest) { m.Engine = "wasm" },
		"capability": func(m *Manifest) { m.Capabilities = []Capability{"Media Requests"} },
		"no schema":  func(m *Manifest) { m.ConfigSchema = nil },
		"not object": func(m *Manifest) { m.ConfigSchema = String("x") },
		"nested writeOnly": func(m *Manifest) {
			m.ConfigSchema = Object(Prop("auth", Object(Prop("token", Secret("Token")))))
		},
		"writeOnly number": func(m *Manifest) {
			m.ConfigSchema = Object(Prop("n", &Schema{Type: "integer", WriteOnly: true}))
		},
		"bad pattern":   func(m *Manifest) { m.ConfigSchema = Object(Prop("x", &Schema{Type: "string", Pattern: "("})) },
		"bad format":    func(m *Manifest) { m.ConfigSchema = Object(Prop("x", &Schema{Type: "string", Format: "email"})) },
		"required":      func(m *Manifest) { m.ConfigSchema = Object().Require("url") },
		"array items":   func(m *Manifest) { m.ConfigSchema = Object(Prop("x", &Schema{Type: "array"})) },
		"property name": func(m *Manifest) { m.ConfigSchema = Object(Prop("API-Key", String("x"))) },
		"duplicate":     func(m *Manifest) { m.ConfigSchema = Object(URLProp(), URLProp()) },
		"type":          func(m *Manifest) { m.ConfigSchema = Object(Prop("x", &Schema{Type: "date"})) },
		"tls shape":     func(m *Manifest) { m.ConfigSchema = Object(Prop("tls", String("TLS"))) },
	}
	for name, mutate := range bad {
		m := manifest("fake")
		mutate(&m)
		if err := ValidateManifest(m); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if err := ValidateManifest(manifest("fake")); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaJSON(t *testing.T) {
	s := manifest("x").ConfigSchema
	if err := s.Check(); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	// Properties keep their order, and secrets are marked writeOnly.
	if i, j, k := strings.Index(got, `"url"`), strings.Index(got, `"tls"`), strings.Index(got, `"api_key"`); i < 0 || i > j || j > k {
		t.Fatalf("property order lost: %s", got)
	}
	for _, want := range []string{`"writeOnly":true`, `"additionalProperties":false`, `"required":["url","api_key"]`, `"format":"uri"`, `"enum":["verify","pin","tofu","insecure"]`} {
		if !strings.Contains(got, want) {
			t.Errorf("schema lacks %s: %s", want, got)
		}
	}
	var generic map[string]any
	if err := json.Unmarshal(b, &generic); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
}

func TestSchemaValidate(t *testing.T) {
	min1 := 1
	s := Object(
		URLProp(),
		TLSProp(),
		Prop("api_key", Secret("Key")),
		Prop("mode", Enum("Mode", "a", "b")),
		Prop("port", Integer("Port", 1, 65535)),
		Prop("ratio", &Schema{Type: "number", Minimum: ptr(0.0), Maximum: ptr(1.0)}),
		Prop("on", Boolean("On")),
		Prop("host", &Schema{Type: "string", Format: "hostname"}),
		Prop("list", &Schema{Type: "array", Items: Object(Prop("name", String("Name"))).Require("name"), MinItems: &min1}),
	).Require("url", "api_key")
	if err := s.Check(); err != nil {
		t.Fatal(err)
	}
	ok := map[string]any{
		"url": "https://sonarr.lab.example:8989/", "api_key": "k", "mode": "a", "port": 8989.0, "ratio": 0.5, "on": true,
		"host": "10.0.11.5", "list": []any{map[string]any{"name": "x"}}, "tls": map[string]any{"mode": "tofu"},
	}
	if errs := s.Validate(ok); len(errs) != 0 {
		t.Fatalf("valid config: %v", errs)
	}
	cases := []struct {
		patch map[string]any
		path  string
	}{
		{map[string]any{"url": nil}, "/url"},
		{map[string]any{"url": "sonarr.lab.example"}, "/url"},
		{map[string]any{"url": "https://user:pass@sonarr.lab.example"}, "/url"},
		{map[string]any{"api_key": ""}, "/api_key"},
		{map[string]any{"mode": "c"}, "/mode"},
		{map[string]any{"port": 8989.5}, "/port"},
		{map[string]any{"port": 70000.0}, "/port"},
		{map[string]any{"port": "8989"}, "/port"},
		{map[string]any{"ratio": -1.0}, "/ratio"},
		{map[string]any{"on": "yes"}, "/on"},
		{map[string]any{"host": "a host"}, "/host"},
		{map[string]any{"list": []any{}}, "/list"},
		{map[string]any{"list": []any{map[string]any{}}}, "/list/0/name"},
		{map[string]any{"list": []any{map[string]any{"name": "x", "extra": 1.0}}}, "/list/0/extra"},
		{map[string]any{"extra": true}, "/extra"},
		{map[string]any{"tls": map[string]any{"mode": "trust"}}, "/tls/mode"},
		{map[string]any{"tls": map[string]any{"mode": "pin", "pin": "nothex"}}, "/tls/pin"},
	}
	for _, c := range cases {
		cfg := map[string]any{}
		for k, v := range ok {
			cfg[k] = v
		}
		for k, v := range c.patch {
			if v == nil {
				delete(cfg, k)
			} else {
				cfg[k] = v
			}
		}
		errs := s.Validate(cfg)
		if len(errs) != 1 || errs[0].Path != c.path {
			t.Errorf("%v: errors = %v, want one at %s", c.patch, errs, c.path)
		}
	}
	if errs := s.Validate("nope"); len(errs) != 1 || errs[0].Path != "" {
		t.Errorf("non-object: %v", errs)
	}
}

func ptr[T any](v T) *T { return &v }

func TestTLSPolicy(t *testing.T) {
	hex := strings.Repeat("ab", 32)
	colons := strings.TrimSuffix(strings.Repeat("AB:", 32), ":")
	for in, want := range map[string]string{hex: hex, colons: hex, " " + strings.ToUpper(hex) + " ": hex} {
		got, err := NormalizePin(in)
		if err != nil || got != want {
			t.Errorf("NormalizePin(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := NormalizePin("abc"); err == nil {
		t.Error("short pin accepted")
	}
	p, err := ParseTLS(nil)
	if err != nil || p.Mode != TLSVerify {
		t.Fatalf("default = %+v, %v", p, err)
	}
	if _, err := ParseTLS(map[string]any{"mode": "pin"}); err == nil {
		t.Error("pin mode without a pin accepted")
	}
	if _, err := ParseTLS(map[string]any{"mode": "skip"}); err == nil {
		t.Error("unknown mode accepted")
	}
	p, err = ParseTLS(map[string]any{"mode": "tofu", "pin": colons})
	if err != nil || p != (TLSPolicy{Mode: TLSTOFU, Pin: hex}) {
		t.Fatalf("tofu = %+v, %v", p, err)
	}
	c := Config{Values: map[string]any{"tls": map[string]any{"mode": "insecure"}}}
	if c.TLS().Mode != TLSInsecure {
		t.Fatal("Config.TLS")
	}
}

func TestSecretRef(t *testing.T) {
	ref := SecretRef("integration/in_x/api_key/abcd")
	if ref != "${secret:integration/in_x/api_key/abcd}" {
		t.Fatal(ref)
	}
	if name, ok := ParseSecretRef(ref); !ok || name != "integration/in_x/api_key/abcd" {
		t.Fatal(name, ok)
	}
	for _, s := range []string{"", "${secret:}", "secret:x", "${secret:x", "prefix ${secret:x}"} {
		if _, ok := ParseSecretRef(s); ok {
			t.Errorf("%q parsed as a reference", s)
		}
	}
}

func TestClassify(t *testing.T) {
	req := &http.Request{Method: "GET", URL: &url.URL{Path: "/api/v3/system/status", RawQuery: "apikey=hunter2"}}
	resp := func(code int) *http.Response { return &http.Response{StatusCode: code, Request: req} }
	if CheckResponse(resp(204)) != nil {
		t.Fatal("2xx is fine")
	}
	cases := []struct {
		err   error
		code  string
		class Class
	}{
		{CheckResponse(resp(401)), CodeAuth, NeedsRepair},
		{CheckResponse(resp(403)), CodeAuth, NeedsRepair},
		{CheckResponse(resp(502)), CodeUpstream, Transient},
		{fmt.Errorf("sync: %w", &AuthError{}), CodeAuth, NeedsRepair},
		{&RepairError{Message: "Karakeep's API changed"}, CodeAPIChanged, NeedsRepair},
		{&RepairError{Code: CodeUnsupportedVersion, Message: "too old"}, CodeUnsupportedVersion, NeedsRepair},
		{&ConfigError{Message: "no socket"}, CodeInvalidConfig, Permanent},
		{&TLSError{Code: CodeTLSPinMismatch, Fingerprint: "aa", Expected: "bb"}, CodeTLSPinMismatch, NeedsRepair},
		{context.DeadlineExceeded, CodeTimeout, Transient},
		{&url.Error{Op: "Get", URL: "http://10.0.11.5:8989/x?apikey=hunter2", Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}}, CodeUnreachable, Transient},
		{&net.DNSError{Err: "no such host", Name: "nope.lab.example", IsNotFound: true}, CodeUnreachable, Transient},
		{errors.New("unexpected EOF"), CodeUpstream, Transient},
	}
	for _, c := range cases {
		code, class, msg := Classify(c.err)
		if code != c.code || class != c.class || msg == "" {
			t.Errorf("Classify(%v) = %s %d %q, want %s %d", c.err, code, class, msg, c.code, c.class)
		}
		if strings.Contains(msg, "hunter2") {
			t.Errorf("Classify(%v) leaked the query string: %q", c.err, msg)
		}
	}
	if !strings.Contains(CheckResponse(resp(401)).Error(), "GET /api/v3/system/status answered 401 Unauthorized") {
		t.Fatal(CheckResponse(resp(401)))
	}
}

func TestTestResultJSON(t *testing.T) {
	b, _ := json.Marshal(TestResult{OK: true, Version: "4.0.1", Latency: 42 * time.Millisecond})
	if string(b) != `{"ok":true,"version":"4.0.1","latency_ms":42,"grants":[],"warnings":[],"error":null}` {
		t.Fatal(string(b))
	}
	b, _ = json.Marshal(Failed(CheckResponse(&http.Response{StatusCode: 401, Request: &http.Request{Method: "GET", URL: &url.URL{Path: "/x"}}})))
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m["ok"] != false || m["error"].(map[string]any)["code"] != CodeAuth {
		t.Fatal(string(b))
	}
	if WriteGrantWarning([]Grant{{Name: "VM.Audit"}}) != nil {
		t.Fatal("read grants aren't a warning")
	}
	w := WriteGrantWarning([]Grant{{Name: "VM.Audit"}, {Name: "VM.PowerMgmt", Write: true}, {Name: "DISK:CREATE_ANY", Write: true}})
	if w == nil || w.Code != "write_grant" || !strings.Contains(w.Message, "DISK:CREATE_ANY, VM.PowerMgmt") {
		t.Fatalf("write grants: %+v", w)
	}
}

func TestGrantJSON(t *testing.T) {
	b, _ := json.Marshal([]Grant{
		{Name: "VM.Audit"},
		{Name: "VM.PowerMgmt", Write: true},
		{Name: "docker", Access: AccessReadWrite, Detail: "DOCKER:READ_ANY, DOCKER:UPDATE_ANY"},
		{Name: "root@pam", Access: AccessAdmin},
	})
	want := `[{"name":"VM.Audit","access":"read","write":false},` +
		`{"name":"VM.PowerMgmt","access":"write","write":true},` +
		`{"name":"docker","access":"rw","write":true,"detail":"DOCKER:READ_ANY, DOCKER:UPDATE_ANY"},` +
		`{"name":"root@pam","access":"admin","write":true}]`
	if string(b) != want {
		t.Fatalf("grants:\n%s\nwant\n%s", b, want)
	}
	if w := WriteGrantWarning([]Grant{{Name: "root@pam", Access: AccessAdmin}}); w == nil {
		t.Fatal("an admin grant is a write grant")
	}
}

func TestFieldOf(t *testing.T) {
	for err, want := range map[error]string{
		fmt.Errorf("x: %w", &AuthError{Field: "token_secret"}): "token_secret",
		&RepairError{Code: CodeAPIChanged, Field: "url"}:       "url",
		&ConfigError{Message: "no", Field: "endpoint"}:         "endpoint",
		&TLSError{Code: CodeTLSPinMismatch}:                    "tls",
		errors.New("plain"):                                    "",
	} {
		if got := FieldOf(err); got != want {
			t.Errorf("FieldOf(%v) = %q, want %q", err, got, want)
		}
	}
}

func TestNumJSON(t *testing.T) {
	b, _ := json.Marshal([]Num{1.5, Num(math.NaN()), Num(math.Inf(1)), Num(math.Copysign(0, -1))})
	if string(b) != `[1.5,null,null,0]` {
		t.Fatal(string(b))
	}
	var back []Num
	if err := json.Unmarshal([]byte(`[2,null]`), &back); err != nil || back[0] != 2 || back[1].Valid() {
		t.Fatal(back, err)
	}
}

// TestIncidentShape pins the inbox shape to the simulator's: an incident
// round-trips through sim.Incident field for field.
func TestIncidentShape(t *testing.T) {
	at := time.Date(2026, 10, 3, 13, 27, 0, 0, time.UTC)
	in := Incident{
		ID: "in_x.repair", Scenario: "auth-rejected", Class: ClassRepair, Severity: SeverityAttention, Category: CategorySecurity, Status: StatusOpen,
		Phase: "rejected", Title: "Plex rejected its credential", Summary: "…", Target: "integration:in_x",
		OpenedAt: at, UpdatedAt: at, ResolvedAt: at.Add(time.Hour), Facts: map[string]any{"code": "auth"},
		Causes:      []Cause{{Text: "401", Resource: "integration:in_x", At: at}},
		BlastRadius: []Impact{{Resource: "app:plex", Via: "monitors", Depth: 1}},
		Suggested:   Action{ID: "integration.reconnect", Label: "Reconnect Plex", Tier: "T0", Target: "integration:in_x", Detail: "d"},
		Alternatives: []Action{{ID: "link.open", Label: "Open Plex", Tier: "T0", Target: "app:plex",
			Impact: []Impact{{Resource: "app:plex", Via: "runs_on", Depth: 1}}}},
		ResolvesWhen: "…", ResolvedBy: "auto", Resolution: "…",
	}
	ours, _ := json.Marshal(in)
	dec := json.NewDecoder(strings.NewReader(string(ours)))
	dec.DisallowUnknownFields()
	var theirs sim.Incident
	if err := dec.Decode(&theirs); err != nil {
		t.Fatalf("our incident isn't a sim incident: %v", err)
	}
	back, _ := json.Marshal(theirs)
	if string(back) != string(ours) {
		t.Fatalf("shapes differ:\nours   %s\ntheirs %s", ours, back)
	}
	// The other way: every field the simulator writes is one we have.
	full, _ := json.Marshal(sim.Incident{Category: sim.CategorySecurity, Facts: map[string]any{"a": 1}, Alternatives: []sim.Action{{}}, ResolvedBy: "x", Resolution: "y", ResolvedAt: at})
	dec = json.NewDecoder(strings.NewReader(string(full)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&Incident{}); err != nil {
		t.Fatalf("a sim incident has fields we lack: %v", err)
	}
}
