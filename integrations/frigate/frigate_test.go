package frigate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

// The fixtures are spike 4.5's recording of a Frigate 0.18 on its internal
// port (testdata/integrations/frigate). They hold no pictures, and the fake
// server below fails the test on any request outside the metadata API, so a
// picture can't be fetched by accident either.
var fixtures = filepath.Join("..", "..", "testdata", "integrations", "frigate")

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtures, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fake is a Frigate serving the fixtures. With auth set it behaves like the
// authenticated port: /api/login sets a cookie every other route needs.
type fake struct {
	*httptest.Server
	t    *testing.T
	auth bool

	mu       sync.Mutex
	files    map[string][]byte // path → body override
	token    string
	logins   int
	hits     map[string]int
	events   []string // the query of each /api/events request
	wsCookie string
	push     chan string // wire frames for /ws after the onConnect replay
}

func newFake(t *testing.T, auth bool) *fake {
	f := &fake{t: t, auth: auth, files: map[string][]byte{}, hits: map[string]int{}, push: make(chan string, 16)}
	f.Server = httptest.NewServer(f)
	t.Cleanup(f.Close)
	return f
}

func (f *fake) set(path string, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[path] = body
}

func (f *fake) loginCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logins
}

func (f *fake) eventQueries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.events)
}

func (f *fake) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[path]
}

// expire forgets the session token, as a Frigate restart or an expired JWT does.
func (f *fake) expire() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.token = "expired"
}

func (f *fake) body(path, file string) []byte {
	f.mu.Lock()
	b, ok := f.files[path]
	f.mu.Unlock()
	if ok {
		return b
	}
	return fixture(f.t, file)
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.hits[r.URL.Path]++
	f.mu.Unlock()
	if r.URL.Path == "/api/login" {
		var body struct{ User, Password string }
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil || body.User != "dash" || body.Password != "viewer-pass" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"message":"Login failed"}`)
			return
		}
		f.mu.Lock()
		f.logins++
		f.token = "jwt-" + strconv.Itoa(f.logins)
		tok := f.token
		f.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "frigate_token", Value: tok, HttpOnly: true, Path: "/"})
		return
	}
	if f.auth {
		f.mu.Lock()
		tok := f.token
		f.mu.Unlock()
		if c, err := r.Cookie("frigate_token"); err != nil || c.Value != tok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}
	if r.Method != http.MethodGet {
		f.t.Errorf("%s %s: the integration only reads", r.Method, r.URL.Path)
	}
	switch r.URL.Path {
	case "/api/version":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write(f.body(r.URL.Path, "version.txt"))
	case "/api/profile":
		b := f.body(r.URL.Path, "profile.json")
		if len(b) == 0 {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	case "/api/config":
		w.Header().Set("Content-Type", "application/json")
		w.Write(f.body(r.URL.Path, "config.json"))
	case "/api/stats":
		w.Header().Set("Content-Type", "application/json")
		w.Write(f.body(r.URL.Path, "stats.json"))
	case "/api/events":
		q := r.URL.Query()
		if q.Get("include_thumbnails") != "0" {
			f.t.Errorf("events without include_thumbnails=0: %s", r.URL.RawQuery)
		}
		f.mu.Lock()
		f.events = append(f.events, r.URL.RawQuery)
		f.mu.Unlock()
		var all []map[string]any
		if err := json.Unmarshal(f.body(r.URL.Path, "events.json"), &all); err != nil {
			f.t.Fatal(err)
		}
		after, _ := strconv.ParseFloat(q.Get("after"), 64)
		limit, _ := strconv.Atoi(q.Get("limit"))
		out := []map[string]any{}
		for _, e := range all {
			if e["start_time"].(float64) > after && len(out) < limit {
				out = append(out, e)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	case "/ws":
		f.serveWS(w, r)
	default:
		f.t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		http.NotFound(w, r)
	}
}

// serveWS answers onConnect with the recorded state (ws_onconnect.json),
// then sends whatever the test pushes. Closing push closes the socket.
func (f *fake) serveWS(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("frigate_token"); err == nil {
		f.mu.Lock()
		f.wsCookie = c.Value
		f.mu.Unlock()
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := r.Context()
	_, first, err := conn.Read(ctx)
	if err != nil {
		return
	}
	if string(first) != onConnect {
		f.t.Errorf("the integration sent %s; only onConnect is allowed", first)
		return
	}
	var rec struct {
		Messages []struct {
			Topic   string `json:"topic"`
			Payload any    `json:"payload"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(fixture(f.t, "ws_onconnect.json"), &rec); err != nil {
		f.t.Error(err)
		return
	}
	for _, m := range rec.Messages {
		if err := conn.Write(ctx, websocket.MessageText, []byte(frame(m.Topic, m.Payload))); err != nil {
			return
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case fr, ok := <-f.push:
			if !ok {
				conn.Close(websocket.StatusGoingAway, "restarting")
				return
			}
			if err := conn.Write(ctx, websocket.MessageText, []byte(fr)); err != nil {
				return
			}
		}
	}
}

// frame is a message as Frigate sends it: the payload is a string, with
// JSON documents encoded inside it.
func frame(topic string, payload any) string {
	s, ok := payload.(string)
	if !ok {
		b, _ := json.Marshal(payload)
		s = string(b)
	}
	b, _ := json.Marshal(map[string]any{"topic": topic, "payload": s, "retain": false})
	return string(b)
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func mustSync(t *testing.T, inst integration.Instance, want integration.Interest) {
	t.Helper()
	if err := inst.Sync(t.Context(), want); err != nil {
		t.Fatal(err)
	}
}

func TestRegistered(t *testing.T) {
	_, m, ok := integration.Default.Get(ID)
	if !ok || m.Name != "Frigate" || !slices.Equal(m.SecretFields(), []string{"password"}) || m.Poll.Interval != 30*time.Second {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestValidate(t *testing.T) {
	// The Homepage importer's shape (testdata/importers/homepage/dogfood).
	imported := map[string]any{"tls": map[string]any{"mode": "verify"}, "url": "http://10.0.11.202:55000"}
	if errs := integrationtest.Validate(Integration{}, imported, nil); len(errs) != 0 {
		t.Fatalf("importer shape: %v", errs)
	}
	withUser := map[string]any{"url": "https://nvr.lab.example:8971", "username": "dash"}
	if errs := integrationtest.Validate(Integration{}, withUser, map[string]string{"password": "x"}); len(errs) != 0 {
		t.Fatalf("with a user: %v", errs)
	}
	cases := map[string]struct {
		config  map[string]any
		secrets map[string]string
		path    string
	}{
		"no url":                {map[string]any{}, nil, "/url"},
		"username, no password": {map[string]any{"url": "http://a.lab.example", "username": "dash"}, nil, "/password"},
		"password, no username": {map[string]any{"url": "http://a.lab.example"}, map[string]string{"password": "x"}, "/username"},
		"rtsp url":              {map[string]any{"url": "rtsp://a.lab.example"}, nil, "/url"},
		"unknown key":           {map[string]any{"url": "http://a.lab.example", "api_key": "x"}, nil, "/api_key"},
	}
	for name, c := range cases {
		errs := integrationtest.Validate(Integration{}, c.config, c.secrets)
		if len(errs) == 0 || errs[0].Path != c.path {
			t.Errorf("%s: %v, want an error at %s", name, errs, c.path)
		}
	}
}

// TestInternalPort is the dogfood setup: the internal port, no sign-in,
// anonymous admin.
func TestInternalPort(t *testing.T) {
	f := newFake(t, false)
	h := integrationtest.New(t)
	inst := h.Connect(Integration{}, map[string]any{"url": f.URL}, nil)
	mustSync(t, inst, integrationtest.Watched)

	app, ok := h.Resource("frigate:i_test/app")
	if !ok || app.Kind != "app" || app.Name != "Test" || app.State != "running" || app.Health != integration.HealthOK || app.Source != "integration:i_test" {
		t.Fatalf("app = %+v", app)
	}
	for k, want := range map[string]any{"type": "frigate", "version": "0.18.0", "cameras": 3.0, "detector": "edgetpu", "url": f.URL,
		"started_at": time.Unix(1791079164-1155815, 0).UTC().Format(time.RFC3339)} {
		if app.Attrs[k] != want {
			t.Errorf("app.attrs.%s = %v, want %v", k, app.Attrs[k], want)
		}
	}
	if _, ok := app.Attrs["latest_version"]; ok {
		t.Error("latest_version is only shown when it's newer")
	}

	drive, ok := h.Resource("frigate:i_test/camera/driveway")
	if !ok || drive.Kind != "camera" || drive.Name != "Driveway" || drive.State != "online" || drive.Health != integration.HealthOK {
		t.Fatalf("driveway = %+v", drive)
	}
	for k, want := range map[string]any{"ip": "10.0.21.16", "fps": 5.0, "detect": true, "record": true, "camera": "driveway",
		"last_event.label": "person", "last_event.at": "2026-10-03T01:10:36Z", "last_event.score": 0.78,
		"last_person.at": "2026-10-03T01:10:36Z", "last_person.score": 0.78} {
		if drive.Attrs[k] != want {
			t.Errorf("driveway.attrs.%s = %v, want %v", k, drive.Attrs[k], want)
		}
	}
	if _, ok := drive.Attrs["motion"]; ok {
		t.Error("motion is only known over the WebSocket")
	}
	// go2rtc restreams resolve to the camera; ffmpeg: sources too.
	if side, _ := h.Resource("frigate:i_test/camera/side_deck"); side.Name != "Side deck" || side.Attrs["ip"] != "10.0.21.18" {
		t.Fatalf("side deck = %+v", side)
	}
	if back, _ := h.Resource("frigate:i_test/camera/backyard"); back.Attrs["ip"] != "10.0.21.17" || back.Attrs["last_event.at"] != nil {
		t.Fatalf("backyard = %+v (its last event is older than 24 h)", back)
	}
	rels := h.Graph().Relations
	for _, k := range []string{
		"frigate:i_test/app exposes frigate:i_test/camera/driveway",
		"integration:i_test monitors frigate:i_test/app",
		"integration:i_test monitors frigate:i_test/camera/backyard",
	} {
		if rels[k] == nil {
			t.Errorf("missing relation %s", k)
		}
	}

	if se := h.Series("frigate:i_test/app", "inference.ms"); se["value"] != 10.59 || se["unit"] != "ms" || se["max"] != 500.0 {
		t.Fatalf("inference.ms = %v", se)
	}
	for cam, want := range map[string]float64{"driveway": 4, "side_deck": 2, "backyard": 0} {
		if se := h.Series("frigate:i_test/camera/"+cam, "detections.24h"); se["value"] != want || se["unit"] != "count" {
			t.Errorf("%s detections.24h = %v, want %v", cam, se, want)
		}
	}
	if q := f.eventQueries()[0]; !strings.Contains(q, "after="+strconv.FormatInt(h.Now().Add(-24*time.Hour).Unix(), 10)) || !strings.Contains(q, "limit=500") {
		t.Fatalf("events query = %s", q)
	}

	// The anonymous admin raises the quiet hint.
	hint, ok := h.Incident(hintID)
	if !ok || hint.Class != "problem" || hint.Severity != "info" || hint.Status != "open" || hint.Target != "frigate:i_test/app" ||
		!strings.Contains(hint.Summary, "delete recordings") || hint.Suggested.Label != "Use the authenticated port" {
		t.Fatalf("hint = %+v %v", hint, ok)
	}

	// Test reports the admin grant honestly.
	res := inst.Test(t.Context())
	if !res.OK || res.Version != "0.18.0" || len(res.Grants) != 1 || res.Grants[0].Access != integration.AccessAdmin ||
		!strings.Contains(res.Grants[0].Detail, "anonymous") || len(res.Warnings) != 1 || res.Warnings[0].Code != "frigate_unauthenticated" {
		t.Fatalf("test = %+v", res)
	}
}

func TestRefreshPlan(t *testing.T) {
	f := newFake(t, false)
	h := integrationtest.New(t)
	inst := h.Connect(Integration{}, map[string]any{"url": f.URL}, nil)
	mustSync(t, inst, integrationtest.Watched)
	h.Advance(30 * time.Second)
	mustSync(t, inst, integrationtest.Watched)
	// The second sync reads only the version and the stats.
	for path, want := range map[string]int{"/api/version": 2, "/api/stats": 2, "/api/config": 1, "/api/profile": 1, "/api/events": 1} {
		if n := f.count(path); n != want {
			t.Errorf("%s read %d times, want %d", path, n, want)
		}
	}
	h.Advance(5 * time.Minute)
	mustSync(t, inst, integrationtest.Watched)
	if f.count("/api/events") != 2 {
		t.Fatal("events are re-read every 5 minutes while watched")
	}
	h.Advance(5 * time.Minute)
	mustSync(t, inst, integration.Interest{})
	if f.count("/api/events") != 2 {
		t.Fatal("idle syncs re-read events every 30 minutes")
	}
	h.Advance(5 * time.Minute)
	mustSync(t, inst, integration.Interest{})
	if f.count("/api/config") != 2 || f.count("/api/profile") != 2 {
		t.Fatal("the config and profile are re-read every 15 minutes")
	}
	// A failed sync makes the next one read everything.
	f.set("/api/stats", []byte("{"))
	h.Advance(time.Minute)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err == nil {
		t.Fatal("a broken stats document should fail the sync")
	}
	f.set("/api/stats", fixture(t, "stats.json"))
	events := f.count("/api/events")
	h.Advance(time.Minute)
	mustSync(t, inst, integrationtest.Watched)
	if f.count("/api/events") != events+1 {
		t.Fatal("after a failure, the next sync reads everything")
	}
}

// TestAuthenticatedPort: a viewer account on the authenticated port signs
// in once, keeps its cookie, signs in again when the token expires, and
// raises no hint.
func TestAuthenticatedPort(t *testing.T) {
	f := newFake(t, true)
	f.set("/api/profile", []byte(`{"username":"dash","role":"viewer","allowed_cameras":["driveway","side_deck"]}`))
	h := integrationtest.New(t)
	inst := h.Connect(Integration{}, map[string]any{"url": f.URL + "/", "username": "dash"}, map[string]string{"password": "viewer-pass"})
	mustSync(t, inst, integrationtest.Watched)
	if f.loginCount() != 1 {
		t.Fatalf("logins = %d", f.loginCount())
	}
	if _, ok := h.Incident(hintID); ok {
		t.Fatal("a signed-in connection raises no hint")
	}
	// The viewer's role is limited to two cameras.
	if _, ok := h.Resource("frigate:i_test/camera/backyard"); ok {
		t.Fatal("a camera the role can't see isn't published")
	}
	if _, ok := h.Resource("frigate:i_test/camera/driveway"); !ok {
		t.Fatal("driveway is allowed")
	}
	f.expire()
	h.Advance(30 * time.Second)
	mustSync(t, inst, integrationtest.Watched)
	if f.loginCount() != 2 {
		t.Fatalf("an expired token should sign in again: logins = %d", f.loginCount())
	}
	res := inst.Test(t.Context())
	if !res.OK || len(res.Grants) != 1 || res.Grants[0].Name != "Frigate viewer" || res.Grants[0].CanWrite() || len(res.Warnings) != 0 ||
		!strings.Contains(res.Grants[0].Detail, "2 cameras") {
		t.Fatalf("test = %+v", res)
	}

	// The WebSocket carries the session cookie too.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- inst.(integration.Subscriber).Subscribe(ctx, h.Emitter) }()
	eventually(t, "the socket", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.wsCookie == f.token
	})
	cancel()
	<-done

	// A wrong password is an auth repair on the password.
	h2 := integrationtest.New(t)
	bad := h2.Connect(Integration{}, map[string]any{"url": f.URL, "username": "dash"}, map[string]string{"password": "nope"})
	err := bad.Sync(t.Context(), integrationtest.Watched)
	if code, class, _ := integration.Classify(err); code != integration.CodeAuth || class != integration.NeedsRepair || integration.FieldOf(err) != "password" {
		t.Fatalf("wrong password: %v (%s)", err, code)
	}
	if res := bad.Test(t.Context()); res.OK || res.ErrorCode != integration.CodeAuth {
		t.Fatalf("test with a wrong password = %+v", res)
	}
	if strings.Contains(h2.Logs(), "nope") {
		t.Fatal("the password leaked into the logs")
	}
}

func TestCameraHealth(t *testing.T) {
	f := newFake(t, false)
	var st map[string]any
	json.Unmarshal(fixture(t, "stats.json"), &st)
	cams := st["cameras"].(map[string]any)
	cams["backyard"].(map[string]any)["camera_fps"] = 0.0
	cams["side_deck"].(map[string]any)["connection_quality"] = "poor"
	cams["side_deck"].(map[string]any)["reconnects_last_hour"] = 2.0
	b, _ := json.Marshal(st)
	f.set("/api/stats", b)
	var cfg map[string]any
	json.Unmarshal(fixture(t, "config.json"), &cfg)
	cfg["cameras"].(map[string]any)["driveway"].(map[string]any)["enabled"] = false
	b, _ = json.Marshal(cfg)
	f.set("/api/config", b)

	h := integrationtest.New(t)
	inst := h.Connect(Integration{}, map[string]any{"url": f.URL}, nil)
	mustSync(t, inst, integration.Interest{})
	if r, _ := h.Resource("frigate:i_test/camera/backyard"); r.State != "error" || r.Health != integration.HealthDown || r.Attrs["health.reason"] != "No frames from the camera" {
		t.Errorf("no frames = %+v", r)
	}
	if r, _ := h.Resource("frigate:i_test/camera/side_deck"); r.State != "online" || r.Health != integration.HealthAttention ||
		r.Attrs["health.reason"] != "Connection quality is poor; reconnected 2 times in the last hour" {
		t.Errorf("poor connection = %+v", r)
	}
	if r, _ := h.Resource("frigate:i_test/camera/driveway"); r.State != "stopped" || r.Health != integration.HealthOK {
		t.Errorf("disabled = %+v", r)
	}
}

func TestWebSocket(t *testing.T) {
	f := newFake(t, false)
	h := integrationtest.New(t)
	inst := h.Connect(Integration{}, map[string]any{"url": f.URL}, nil)
	mustSync(t, inst, integrationtest.Watched)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- inst.(integration.Subscriber).Subscribe(ctx, h.Emitter) }()

	attr := func(cam, key string) any {
		r, _ := h.Resource("frigate:i_test/camera/" + cam)
		return r.Attrs[key]
	}
	// The recorded state: camera_activity, heartbeats, and motion on the
	// side deck that has stopped by the end of the recording.
	eventually(t, "the recorded state", func() bool {
		return attr("side_deck", "motion") == false && attr("driveway", "motion") == false && attr("backyard", "motion") == false
	})
	f.push <- frame("side_deck/motion", "ON")
	eventually(t, "motion", func() bool { return attr("side_deck", "motion") == true })

	// A stream goes offline: down at once, without waiting for stats.
	f.push <- frame("backyard/status/detect", "offline")
	eventually(t, "offline", func() bool {
		r, _ := h.Resource("frigate:i_test/camera/backyard")
		return r.Health == integration.HealthDown && r.State == "error"
	})

	// The stats push updates inference.ms, and the next syncs skip /api/stats.
	var passive struct {
		Messages []struct {
			Topic   string `json:"topic"`
			Payload any    `json:"payload"`
		} `json:"messages"`
	}
	json.Unmarshal(fixture(t, "ws_passive.json"), &passive)
	var stats map[string]any
	for _, m := range passive.Messages {
		if m.Topic == "stats" {
			stats = m.Payload.(map[string]any)
		}
	}
	stats["detectors"].(map[string]any)["coral"].(map[string]any)["inference_speed"] = 12.25
	f.push <- frame("stats", stats)
	eventually(t, "the stats push", func() bool { return h.Series("frigate:i_test/app", "inference.ms")["value"] == 12.25 })
	before := f.count("/api/stats")
	h.Advance(30 * time.Second)
	mustSync(t, inst, integrationtest.Watched)
	if f.count("/api/stats") != before {
		t.Fatal("stats are pushed; the sync shouldn't poll them")
	}
	if r, _ := h.Resource("frigate:i_test/camera/backyard"); r.Health != integration.HealthDown {
		t.Fatalf("the WebSocket's status wins over older stats: %+v", r)
	}

	// A new tracked object updates the caption now and the count on the
	// next sync. Its sub_label is never kept.
	at := float64(h.Now().Unix() - 60)
	f.push <- frame("events", map[string]any{"type": "new", "before": map[string]any{}, "after": map[string]any{
		"camera": "backyard", "label": "dog", "sub_label": "known_face_1", "top_score": 0.91, "start_time": at}})
	eventually(t, "the new event", func() bool { return attr("backyard", "last_event.label") == "dog" })
	if attr("backyard", "last_event.score") != 0.91 || attr("backyard", "last_person.at") != nil {
		t.Fatalf("backyard = %v", attr("backyard", "last_event.score"))
	}
	if strings.Contains(fmt.Sprint(h.Graph()), "known_face_1") {
		t.Fatal("sub_label must never be published")
	}
	events := f.count("/api/events")
	h.Advance(eventsDebounce)
	mustSync(t, inst, integrationtest.Watched)
	if f.count("/api/events") != events+1 {
		t.Fatal("a new object makes the next sync recount")
	}

	// Frigate restarts: the socket closes and polling takes over again.
	close(f.push)
	if err := <-done; err == nil {
		t.Fatal("Subscribe returns when the socket closes")
	}
	if r, _ := h.Resource("frigate:i_test/camera/backyard"); r.Health != integration.HealthOK || r.Attrs["motion"] != nil {
		t.Fatalf("after the socket closed: %+v", r)
	}
	h.Advance(30 * time.Second)
	mustSync(t, inst, integrationtest.Watched)
	if f.count("/api/stats") != before+1 || f.count("/api/config") != 2 {
		t.Fatal("without the socket, stats are polled (and opening it made one sync re-read the config)")
	}
}

// TestHintLifecycle: Frigate before 0.14 has no users at all; adding
// credentials (on a newer Frigate) restarts the instance with the same
// emitter, and the new instance resolves the hint.
func TestHintLifecycle(t *testing.T) {
	old := newFake(t, false)
	old.set("/api/profile", []byte{}) // 404
	h := integrationtest.New(t)
	mustSync(t, h.Connect(Integration{}, map[string]any{"url": old.URL}, nil), integrationtest.Watched)
	if in, ok := h.Incident(hintID); !ok || in.Status != "open" {
		t.Fatalf("no users at all = %+v", in)
	}
	if res := h.Connect(Integration{}, map[string]any{"url": old.URL}, nil).Test(t.Context()); !res.OK || res.Grants[0].Name != "Frigate API" || !res.Grants[0].CanWrite() {
		t.Fatalf("test = %+v", res)
	}
	signed := newFake(t, true)
	signed.set("/api/profile", []byte(`{"username":"dash","role":"viewer","allowed_cameras":[]}`))
	mustSync(t, h.Connect(Integration{}, map[string]any{"url": signed.URL, "username": "dash"}, map[string]string{"password": "viewer-pass"}), integrationtest.Watched)
	if in, _ := h.Incident(hintID); in.Status != "resolved" || in.ResolvedBy != "auto" {
		t.Fatalf("after signing in = %+v", in)
	}
}

func TestNotFrigate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, "<html>sign in</html>")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	h := integrationtest.New(t)
	inst := h.Connect(Integration{}, map[string]any{"url": srv.URL}, nil)
	err := inst.Sync(t.Context(), integrationtest.Watched)
	var re *integration.RepairError
	if !errors.As(err, &re) || re.Code != integration.CodeAPIChanged {
		t.Fatalf("an HTML page = %v", err)
	}
	inst = h.Connect(Integration{}, map[string]any{"url": srv.URL + "/nvr"}, nil)
	if code, _, _ := integration.Classify(inst.Sync(t.Context(), integrationtest.Watched)); code != integration.CodeAPIChanged {
		t.Fatalf("a 404 = %s", code)
	}
}

func TestHostOf(t *testing.T) {
	for in, want := range map[string]string{
		"rtsp://*:*@10.0.21.16:554/Preview_01_main":                      "10.0.21.16",
		"ffmpeg:http://10.0.21.17/flv?port=1935&user=*&password=*":       "10.0.21.17",
		"rtsp://cam.lab.example/live":                                    "cam.lab.example",
		"rtsp://127.0.0.1:8554/driveway":                                 "",
		"ffmpeg:driveway#audio=aac":                                      "",
		"rtsp://localhost:8554/x":                                        "",
		"not a url":                                                      "",
		"rtsp://user:hunter2@10.0.21.30:554/stream?password=hunter2#x=1": "10.0.21.30",
	} {
		if got := hostOf(in); got != want {
			t.Errorf("hostOf(%q) = %q, want %q", in, got, want)
		}
	}
}
