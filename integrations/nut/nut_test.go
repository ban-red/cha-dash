package nut

import (
	"bufio"
	"context"
	"encoding/json"
	"math"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

// Transcripts written from NUT's network protocol docs (no NUT server was
// at hand to record one); see testdata/integrations/nut/README.md.
const fixtures = "../../testdata/integrations/nut"

const user, password = "monuser", "fake-upsd-password-7Q2"

// transcript is a fixture: what the client sends ("> ") and upsd's answer
// lines ("< ").
type transcript struct {
	sends   []string
	answers map[string][]string
}

func load(t *testing.T, name string) transcript {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtures, name))
	if err != nil {
		t.Fatal(err)
	}
	tr := transcript{answers: map[string][]string{}}
	cur := ""
	for line := range strings.SplitSeq(string(b), "\n") {
		switch {
		case strings.HasPrefix(line, "> "):
			cur = line[2:]
			tr.sends = append(tr.sends, cur)
			tr.answers[cur] = nil
		case strings.HasPrefix(line, "< "):
			tr.answers[cur] = append(tr.answers[cur], line[2:])
		case line == "", strings.HasPrefix(line, "#"):
		default:
			t.Fatalf("%s: a line that's neither > nor <: %q", name, line)
		}
	}
	return tr
}

// upsd is a fake upsd on a loopback TCP listener, answering from a
// transcript. USERNAME and PASSWORD it handles itself.
type upsd struct {
	t  *testing.T
	ln net.Listener

	mu       sync.Mutex
	tr       transcript
	password string // when set, PASSWORD with anything else is ERR INVALID-PASSWORD
	deny     bool   // every LIST answers ERR ACCESS-DENIED
	got      [][]string
}

func newUpsd(t *testing.T, fixture string) *upsd {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	u := &upsd{t: t, ln: ln, tr: load(t, fixture)}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go u.serve(c)
		}
	}()
	return u
}

func (u *upsd) config() map[string]any {
	_, port, _ := net.SplitHostPort(u.ln.Addr().String())
	p, _ := strconv.Atoi(port)
	return map[string]any{"host": "127.0.0.1", "port": p}
}

func (u *upsd) use(fixture string) {
	tr := load(u.t, fixture)
	u.mu.Lock()
	defer u.mu.Unlock()
	u.tr = tr
}

// sessions returns each connection's commands, LOGOUT included.
func (u *upsd) sessions() [][]string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.got)
}

func (u *upsd) serve(c net.Conn) {
	defer c.Close()
	u.mu.Lock()
	n := len(u.got)
	u.got = append(u.got, nil)
	u.mu.Unlock()
	r := bufio.NewScanner(c)
	for r.Scan() {
		line := r.Text()
		u.mu.Lock()
		u.got[n] = append(u.got[n], line)
		tr, want, deny := u.tr, u.password, u.deny
		u.mu.Unlock()
		var out []string
		switch {
		case line == "LOGOUT":
			c.Write([]byte("OK Goodbye\n"))
			return
		case strings.HasPrefix(line, "USERNAME "):
			out = []string{"OK"}
		case strings.HasPrefix(line, "PASSWORD "):
			words, _ := split(line)
			if want != "" && (len(words) != 2 || words[1] != want) {
				out = []string{"ERR INVALID-PASSWORD"}
			} else {
				out = []string{"OK"}
			}
		case deny && strings.HasPrefix(line, "LIST "):
			out = []string{"ERR ACCESS-DENIED"}
		default:
			a, ok := tr.answers[line]
			if !ok {
				a = []string{"ERR UNKNOWN-COMMAND"}
			}
			out = a
		}
		c.Write([]byte(strings.Join(out, "\n") + "\n"))
	}
}

func connect(t *testing.T, u *upsd, creds bool) (*integrationtest.Harness, integration.Instance) {
	h := integrationtest.New(t)
	h.State = &recordingState{}
	cfg, secrets := u.config(), map[string]string{}
	if creds {
		cfg["username"] = user
		secrets["password"] = password
	}
	return h, h.Connect(Integration{}, cfg, secrets)
}

func mustSync(t *testing.T, inst integration.Instance) {
	t.Helper()
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatalf("sync: %v", err)
	}
}

func rid(h *integrationtest.Harness, name string) string { return "nut:" + h.ID + "/ups/" + name }

func value(t *testing.T, h *integrationtest.Harness, res, key string) any {
	t.Helper()
	s := h.Series(res, key)
	if s == nil {
		t.Fatalf("no series %s on %s", key, res)
	}
	return s["value"]
}

func TestOnline(t *testing.T) {
	u := newUpsd(t, "online.txt")
	h, inst := connect(t, u, false)
	mustSync(t, inst)

	// The client sends exactly the transcript's commands, then LOGOUT.
	want := append(slices.Clone(u.tr.sends), "LOGOUT")
	if got := u.sessions(); len(got) != 1 || !slices.Equal(got[0], want) {
		t.Fatalf("sessions = %q, want one of %q", got, want)
	}
	r, ok := h.Resource(rid(h, "rack"))
	if !ok {
		t.Fatalf("no UPS; graph %+v", h.Graph())
	}
	if r.Kind != "ups" || r.Name != "Rack UPS" || r.State != "online" || r.Health != integration.HealthOK {
		t.Fatalf("rack = %+v", r)
	}
	a := r.Attrs
	if a["ups"] != "rack" || a["status"] != "OL" || a["model"] != "5P 1550" || a["manufacturer"] != "EATON" ||
		a["on_battery"] != false || a["low_battery"] != false || a["driver"] != "usbhid-ups" || a["test_result"] != "Done and passed" {
		t.Fatalf("attrs = %v", a)
	}
	if _, ok := a["serial"]; ok {
		t.Fatal("the serial number is published")
	}
	for key, v := range map[string]float64{"battery.pct": 100, "runtime.min": 45.5, "load.pct": 23, "input.v": 231.4, "power.w": 251} {
		if got := value(t, h, string(r.ID), key); got != v {
			t.Errorf("%s = %v, want %v", key, got, v)
		}
	}
	if s := h.Series(string(r.ID), "input.v"); s["max"] != float64(299) {
		t.Errorf("input.v scale from the nominal 230 V: %v", s)
	}
	if _, ok := h.Incident("ups.rack.low_battery"); ok {
		t.Fatal("a low-battery incident on mains")
	}
	found := false
	for _, rel := range h.Graph().Relations {
		if string(rel.From) == h.Emitter.Self() && rel.Type == "monitors" && rel.To == r.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("the instance doesn't monitor the UPS")
	}

	res := inst.Test(t.Context())
	if !res.OK || res.Version != "2.8.2" || len(res.Warnings) != 0 {
		t.Fatalf("test = %+v", res)
	}
}

func TestOnBatteryIsAttentionWithoutAnIncident(t *testing.T) {
	u := newUpsd(t, "online.txt")
	h, inst := connect(t, u, false)
	mustSync(t, inst)
	u.use("on_battery.txt")
	mustSync(t, inst)
	r, _ := h.Resource(rid(h, "rack"))
	if r.State != "on_battery" || r.Health != integration.HealthAttention ||
		r.Attrs["health.reason"] != "on battery: the mains power is out, about 31 minutes left" || r.Attrs["on_battery"] != true {
		t.Fatalf("rack = %+v", r)
	}
	if got := value(t, h, string(r.ID), "input.v"); got != float64(0) {
		t.Fatalf("input.v = %v: a cut is 0 V, not unknown", got)
	}
	if _, ok := h.Incident("ups.rack.low_battery"); ok {
		t.Fatal("on battery alone raised an incident")
	}
}

func TestLowBatteryRaisesAndResolves(t *testing.T) {
	u := newUpsd(t, "low_battery.txt")
	h, inst := connect(t, u, false)
	mustSync(t, inst)
	r, _ := h.Resource(rid(h, "rack"))
	if r.State != "on_battery" || r.Health != integration.HealthAttention || r.Attrs["low_battery"] != true {
		t.Fatalf("rack = %+v", r)
	}
	in, ok := h.Incident("ups.rack.low_battery")
	if !ok {
		t.Fatal("no incident")
	}
	if in.Severity != integration.SeverityAttention || in.Class != integration.ClassProblem || in.Status != integration.StatusOpen ||
		in.Target != string(r.ID) || in.Title != "Rack UPS is on battery, and the battery is low" ||
		!strings.Contains(in.Summary, "18% charge, about 3 minutes of runtime") || in.Scenario != "ups-low-battery" {
		t.Fatalf("incident = %+v", in)
	}
	opened := in.OpenedAt

	// Raised again unchanged while it lasts.
	h.Advance(15 * time.Second)
	mustSync(t, inst)
	if again, _ := h.Incident("ups.rack.low_battery"); !again.OpenedAt.Equal(opened) || again.Summary != in.Summary {
		t.Fatalf("incident changed: %+v", again)
	}

	// Mains back: LB clears, the incident resolves.
	u.use("online.txt")
	h.Advance(time.Minute)
	mustSync(t, inst)
	in, _ = h.Incident("ups.rack.low_battery")
	if in.Status != integration.StatusResolved || !strings.Contains(in.Resolution, "no longer reports a low battery") {
		t.Fatalf("incident = %+v", in)
	}
}

func TestSeveralUPSes(t *testing.T) {
	u := newUpsd(t, "several.txt")
	h, inst := connect(t, u, false)
	mustSync(t, inst)
	desk, _ := h.Resource(rid(h, "desk"))
	if desk.Name != "desk" || desk.Health != integration.HealthOK || desk.Attrs["charging"] != true {
		t.Fatalf("desk = %+v", desk)
	}
	// No ups.realpower: load × nominal. 9% of 405 W.
	if got := value(t, h, string(desk.ID), "power.w"); got != float64(36) {
		t.Errorf("desk power.w = %v", got)
	}
	closet, _ := h.Resource(rid(h, "closet"))
	if closet.Name != `Closet "network" UPS` || closet.Health != integration.HealthAttention ||
		closet.Attrs["health.reason"] != "the battery needs replacing" || closet.Attrs["replace_battery"] != true {
		t.Fatalf("closet = %+v", closet)
	}
	if got := value(t, h, string(closet.ID), "power.w"); got != nil {
		t.Errorf("closet power.w = %v, want unknown", got)
	}
	garage, _ := h.Resource(rid(h, "garage"))
	if garage.State != "error" || garage.Health != integration.HealthAttention || garage.Attrs["error"] != "DATA-STALE" ||
		!strings.Contains(garage.Attrs["health.reason"].(string), "stale") {
		t.Fatalf("garage = %+v", garage)
	}
	if got := value(t, h, string(garage.ID), "battery.pct"); got != nil {
		t.Errorf("garage battery.pct = %v, want unknown", got)
	}
	res := inst.Test(t.Context())
	if !res.OK || len(res.Warnings) != 1 || res.Warnings[0].Code != "ups_unreadable" || !strings.HasPrefix(res.Warnings[0].Message, "garage: ") {
		t.Fatalf("test = %+v", res)
	}
}

func TestCredentials(t *testing.T) {
	u := newUpsd(t, "online.txt")
	u.password = password
	h, inst := connect(t, u, true)
	mustSync(t, inst)
	got := u.sessions()[0]
	if got[0] != `USERNAME "monuser"` || got[1] != `PASSWORD "`+password+`"` {
		t.Fatalf("sent %q", got)
	}
	res := inst.Test(t.Context())
	if !res.OK || len(res.Warnings) != 1 || res.Warnings[0].Code != "plain_tcp" {
		t.Fatalf("test = %+v", res)
	}
	noPassword(t, h, nil, res)
}

func TestWrongPasswordIsARepair(t *testing.T) {
	u := newUpsd(t, "online.txt")
	u.password = "the-right-one"
	h, inst := connect(t, u, true)
	err := inst.Sync(t.Context(), integrationtest.Watched)
	code, class, msg := integration.Classify(err)
	if code != integration.CodeAuth || class != integration.NeedsRepair || integration.FieldOf(err) != "password" ||
		msg != "upsd refused the password (ERR INVALID-PASSWORD)" {
		t.Fatalf("err = %v (%s, %v)", err, code, class)
	}
	res := inst.Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth {
		t.Fatalf("test = %+v", res)
	}
	noPassword(t, h, err, res)
}

func TestAccessDenied(t *testing.T) {
	u := newUpsd(t, "online.txt")
	u.deny = true
	for _, creds := range []bool{false, true} {
		_, inst := connect(t, u, creds)
		err := inst.Sync(t.Context(), integrationtest.Watched)
		code, class, _ := integration.Classify(err)
		field := map[bool]string{false: "username", true: "password"}[creds]
		if code != integration.CodeAuth || class != integration.NeedsRepair || integration.FieldOf(err) != field ||
			!strings.Contains(err.Error(), "LIST UPS (ERR ACCESS-DENIED)") {
			t.Fatalf("creds %v: err = %v, field %q", creds, err, integration.FieldOf(err))
		}
	}
}

// The server away: the sync fails as unreachable, transient, so the runtime
// backs off and marks what was published stale; nothing is removed.
func TestServerAway(t *testing.T) {
	u := newUpsd(t, "online.txt")
	h, inst := connect(t, u, true)
	mustSync(t, inst)
	u.ln.Close()
	err := inst.Sync(t.Context(), integrationtest.Watched)
	code, class, _ := integration.Classify(err)
	if code != integration.CodeUnreachable || class != integration.Transient {
		t.Fatalf("err = %v (%s, %v)", err, code, class)
	}
	if r, ok := h.Resource(rid(h, "rack")); !ok || r.Health != integration.HealthOK {
		t.Fatalf("the last facts went: %+v", r)
	}
	res := inst.Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeUnreachable {
		t.Fatalf("test = %+v", res)
	}
	noPassword(t, h, err, res)
}

// Something else on the port: a repair pointing at the port.
func TestNotUpsd(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				bufio.NewReader(c).ReadString('\n')
				c.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
			}()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := strconv.Atoi(port)
	h := integrationtest.New(t)
	inst := h.Connect(Integration{}, map[string]any{"host": "127.0.0.1", "port": p}, nil)
	err = inst.Sync(t.Context(), integrationtest.Watched)
	if code, class, _ := integration.Classify(err); code != integration.CodeAPIChanged || class != integration.NeedsRepair || integration.FieldOf(err) != "port" {
		t.Fatalf("err = %v", err)
	}
}

// A server that stops answering mid-session times out within the sync's
// deadline.
func TestSilentServerTimesOut(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { c.Close() })
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := strconv.Atoi(port)
	h := integrationtest.New(t)
	inst := h.Connect(Integration{}, map[string]any{"host": "127.0.0.1", "port": p}, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	err = inst.Sync(ctx, integrationtest.Watched)
	if code, class, _ := integration.Classify(err); code != integration.CodeTimeout || class != integration.Transient {
		t.Fatalf("err = %v (%s)", err, code)
	}
}

func TestValidate(t *testing.T) {
	ok := map[string]any{"host": "ups.home.example", "port": 3493}
	if errs := integrationtest.Validate(Integration{}, ok, nil); len(errs) != 0 {
		t.Fatal(errs)
	}
	if errs := integrationtest.Validate(Integration{}, map[string]any{"host": "192.0.2.10"}, nil); len(errs) != 0 {
		t.Fatal(errs)
	}
	for name, c := range map[string]struct {
		cfg     map[string]any
		secrets map[string]string
		path    string
	}{
		"no host":           {map[string]any{}, nil, "/host"},
		"a url":             {map[string]any{"host": "http://192.0.2.10"}, nil, "/host"},
		"password, no user": {map[string]any{"host": "192.0.2.10"}, map[string]string{"password": "x"}, "/username"},
		"two-line password": {map[string]any{"host": "192.0.2.10", "username": "u"}, map[string]string{"password": "a\nLIST UPS"}, "/password"},
		"port out of range": {map[string]any{"host": "192.0.2.10", "port": 70000}, nil, "/port"},
	} {
		errs := integrationtest.Validate(Integration{}, c.cfg, c.secrets)
		if len(errs) == 0 || errs[0].Path != c.path {
			t.Errorf("%s: %v", name, errs)
		}
	}
}

func TestSplit(t *testing.T) {
	for line, want := range map[string][]string{
		`VAR rack ups.status "OB LB"`: {"VAR", "rack", "ups.status", "OB LB"},
		`UPS c "a \"b\" \\ c"`:        {"UPS", "c", `a "b" \ c`},
		`VAR x y ""`:                  {"VAR", "x", "y", ""},
		`ERR DATA-STALE`:              {"ERR", "DATA-STALE"},
		"  spaced\tout  ":             {"spaced", "out"},
		`"one"two`:                    {"one", "two"},
	} {
		got, ok := split(line)
		if !ok || !slices.Equal(got, want) {
			t.Errorf("split(%q) = %q, %v", line, got, ok)
		}
	}
	if _, ok := split(`VAR x y "open`); ok {
		t.Error("an unterminated quote parsed")
	}
	if got, _ := split(quote(`p "w\d`)); len(got) != 1 || got[0] != `p "w\d` {
		t.Errorf("quote round trip = %q", got)
	}
}

func TestAssess(t *testing.T) {
	for status, want := range map[string][2]string{
		"OL":         {"online", integration.HealthOK},
		"OL CHRG":    {"online", integration.HealthOK},
		"OL TRIM":    {"online", integration.HealthOK},
		"OB DISCHRG": {"on_battery", integration.HealthAttention},
		"OB LB":      {"on_battery", integration.HealthAttention},
		"OL CHRG LB": {"online", integration.HealthAttention},
		"OL OVER":    {"online", integration.HealthAttention},
		"OL BYPASS":  {"online", integration.HealthAttention},
		"OL CAL":     {"online", integration.HealthMaintenance},
		"OB LB FSD":  {"on_battery", integration.HealthDown},
		"OFF":        {"off", integration.HealthDown},
		"WAIT":       {"unknown", integration.HealthUnknown},
		"":           {"unknown", integration.HealthUnknown},
	} {
		a := assess(ups{vars: map[string]string{"ups.status": status}})
		if a.state != want[0] || a.health.Status != want[1] {
			t.Errorf("%q = %s/%s, want %v", status, a.state, a.health.Status, want)
		}
	}
	if m := metrics("r", nil); !math.IsNaN(m[0].Value) {
		t.Error("an unread UPS has a charge")
	}
}

func TestSlug(t *testing.T) {
	if s := slug("rack-1.a_b"); s != "rack-1.a_b" {
		t.Error(s)
	}
	if s := slug("a b/c"); s != "a-b-c" {
		t.Error(s)
	}
	if s := slug(strings.Repeat("x", 100)); len(s) != 64 {
		t.Error(len(s))
	}
}

// noPassword checks the password is in no log, error, test result, graph,
// series or state.
func noPassword(t *testing.T, h *integrationtest.Harness, err error, res integration.TestResult) {
	t.Helper()
	b, _ := json.Marshal(res)
	where := map[string]string{"logs": h.Logs(), "test": string(b) + res.ErrorMessage}
	if err != nil {
		where["error"] = err.Error()
	}
	g, _ := json.Marshal(h.Graph())
	where["graph"] = string(g)
	sc, _ := json.Marshal(h.StoredConfig())
	where["stored config"] = string(sc)
	if s := h.Series(rid(h, "rack"), "battery.pct"); s != nil {
		b, _ := json.Marshal(s)
		where["series"] = string(b)
	}
	if st, ok := h.State.(*recordingState); !ok || st.saves() != 0 {
		t.Errorf("the instance saved state (%v)", ok)
	}
	for name, s := range where {
		if strings.Contains(s, password) {
			t.Errorf("the password is in the %s: %s", name, s)
		}
	}
}

// recordingState is an empty State that counts saves.
type recordingState struct {
	mu sync.Mutex
	n  int
}

func (s *recordingState) Load(context.Context, string) ([]byte, error) { return nil, nil }

func (s *recordingState) Save(context.Context, string, []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return nil
}

func (s *recordingState) saves() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

func TestRecipe(t *testing.T) {
	r, ok := integration.Default.Recipe(ID)
	if !ok || r.Access != integration.RecipeReadOnly || len(r.Steps) != 2 || len(r.Notes) == 0 {
		t.Fatalf("recipe = %+v, %v", r, ok)
	}
	if _, m, ok := integration.Default.Get(ID); !ok || m.Poll.Interval != 15*time.Second || !m.Poll.Background {
		t.Fatalf("manifest = %+v", m)
	}
}
