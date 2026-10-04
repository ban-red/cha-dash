package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	return st
}

// newLog returns a log whose clock starts at a fixed time and moves a
// second per event.
func newLog(t *testing.T) (*Log, *store.Store) {
	t.Helper()
	st := openStore(t)
	l := New(st)
	at := time.Date(2026, 10, 4, 14, 0, 0, 0, time.UTC)
	l.SetClock(func() time.Time { at = at.Add(time.Second); return at })
	return l, st
}

var sam = Actor{Kind: "user", ID: "u_sam", Name: "sam", Via: "web", Session: "s_1", IP: "192.0.2.10"}

func appendN(t *testing.T, l *Log, entries ...Entry) []Event {
	t.Helper()
	var out []Event
	for _, e := range entries {
		ev, err := l.Append(t.Context(), e)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, ev)
	}
	return out
}

func TestAppendChains(t *testing.T) {
	l, _ := newLog(t)
	evs := appendN(t, l,
		Entry{Actor: sam, Action: "proxmox.guest.reboot", Target: "proxmox:i_x/qemu/201", Tier: "T2", Result: OK, Duration: 8123 * time.Millisecond, Task: "t_1"},
		Entry{Actor: sam, Action: "docker.container.remove", Target: "docker:i_y/container/old", Tier: "T3", Result: Refused, Error: &Error{Code: "step_up_required", Message: "Confirm it's you first."}},
		Entry{Actor: Actor{Kind: "system", Name: "Cha Dash"}, Action: "settings.security.update", Result: Failed, Error: &Error{Code: "failed", Message: "Nope."}},
	)
	prev := ZeroHash
	for i, ev := range evs {
		if ev.Seq != int64(i+1) || ev.PrevHash != prev || !strings.HasPrefix(ev.ID, "a_") {
			t.Fatalf("event %d: seq %d, prev %s, id %s", i, ev.Seq, ev.PrevHash, ev.ID)
		}
		body, err := Canonical(ev)
		if err != nil || HashOf(body) != ev.Hash {
			t.Fatalf("event %d: hash %s doesn't match its canonical form (%v)", i, ev.Hash, err)
		}
		prev = ev.Hash
	}
	if evs[0].DurationMS != 8123 || string(evs[0].Params) != "{}" {
		t.Fatalf("first event = %+v", evs[0])
	}

	// The canonical form is pinned: members in order, compact, UTC with
	// milliseconds, empty actor members left out.
	body, _ := Canonical(evs[2])
	want := `{"id":"` + evs[2].ID + `","seq":3,"at":"2026-10-04T14:00:03.000Z","actor":{"kind":"system","name":"Cha Dash"},` +
		`"action":"settings.security.update","target":"","params":{},"tier":"","result":"failed","error":{"code":"failed","message":"Nope."},` +
		`"duration_ms":0,"task":"","prev_hash":"` + evs[1].Hash + `"}`
	if string(body) != want {
		t.Fatalf("canonical =\n%s\nwant\n%s", body, want)
	}

	if _, err := l.Append(t.Context(), Entry{Action: "x.y", Result: "maybe"}); err == nil {
		t.Fatal("an unknown result was accepted")
	}
	if _, err := l.Append(t.Context(), Entry{Action: "x.y", Result: OK, Params: json.RawMessage(`[1]`)}); err == nil {
		t.Fatal("params that aren't an object were accepted")
	}
}

func TestParamsCanonicalAndRedacted(t *testing.T) {
	l, st := newLog(t)
	ev := appendN(t, l, Entry{Actor: sam, Action: "users.create", Result: OK,
		Params:  json.RawMessage(`{"username":"kim","password":"FAKE-secret-value","z":{"b":1.50,"a":[2,1]},"role":"viewer"}`),
		Secrets: []string{"password"}})[0]
	want := `{"password":"[redacted]","role":"viewer","username":"kim","z":{"a":[2,1],"b":1.50}}`
	if string(ev.Params) != want {
		t.Fatalf("params = %s, want %s", ev.Params, want)
	}
	var stored string
	if err := st.Reader().QueryRow(`SELECT event FROM audit_events`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "FAKE-secret") {
		t.Fatal("a secret param reached the log")
	}
}

func TestQuery(t *testing.T) {
	l, _ := newLog(t)
	kim := Actor{Kind: "user", ID: "u_kim", Name: "kim"}
	appendN(t, l,
		Entry{Actor: sam, Action: "proxmox.guest.reboot", Target: "a", Result: OK},         // 14:00:01
		Entry{Actor: kim, Action: "proxmox.guest.start", Target: "b", Result: Refused},     // 14:00:02
		Entry{Actor: sam, Action: "docker.container.restart", Target: "a", Result: Failed}, // 14:00:03
		Entry{Actor: sam, Action: "settings.security.update", Result: OK},                  // 14:00:04
		Entry{Actor: kim, Action: "proxmox_x.guest.start", Target: "c", Result: OK},        // 14:00:05
	)
	ctx := t.Context()
	seqs := func(f Filter) []int64 {
		t.Helper()
		p, err := l.Query(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []int64
		for _, e := range p.Events {
			out = append(out, e.Seq)
		}
		return out
	}
	at := func(s int) time.Time { return time.Date(2026, 10, 4, 14, 0, s, 0, time.UTC) }
	for _, c := range []struct {
		f    Filter
		want string
	}{
		{Filter{}, "[5 4 3 2 1]"},
		{Filter{Actor: "KIM"}, "[5 2]"},
		{Filter{Actor: "u_sam"}, "[4 3 1]"},
		{Filter{Action: "proxmox.guest.start"}, "[2]"},
		{Filter{Action: "proxmox."}, "[2 1]"},
		{Filter{Action: "proxmox_*"}, "[5]"},
		{Filter{Target: "a"}, "[3 1]"},
		{Filter{Result: Refused}, "[2]"},
		{Filter{Since: at(2), Before: at(4)}, "[3 2]"},
	} {
		if got := seqs(c.f); jsonString(got) != strings.ReplaceAll(c.want, " ", ",") {
			t.Errorf("Query(%+v) = %v, want %s", c.f, got, c.want)
		}
	}

	// Pages: newest first, next continues, the last page has no next.
	p1, err := l.Query(ctx, Filter{Limit: 2})
	if err != nil || len(p1.Events) != 2 || p1.Next == "" || p1.Events[0].Seq != 5 {
		t.Fatalf("page 1 = %+v, %v", p1, err)
	}
	p2, _ := l.Query(ctx, Filter{Limit: 2, Cursor: p1.Next})
	p3, _ := l.Query(ctx, Filter{Limit: 2, Cursor: p2.Next})
	if p2.Events[0].Seq != 3 || len(p3.Events) != 1 || p3.Events[0].Seq != 1 || p3.Next != "" {
		t.Fatalf("pages 2 and 3 = %+v, %+v", p2, p3)
	}
	if _, err := l.Query(ctx, Filter{Cursor: "nope"}); err != ErrBadCursor {
		t.Fatalf("bad cursor: %v", err)
	}
	// Events read back carry their hash.
	if p1.Events[0].Hash == "" || p1.Events[0].PrevHash != p1.Events[1].Hash {
		t.Fatalf("events read back = %+v", p1.Events)
	}
}

func jsonString(v any) string { b, _ := json.Marshal(v); return string(b) }

// An exported chain checks out without this package: strip the trailing
// hash, hash the rest, compare with the next line's prev_hash.
func TestExportVerifiesOnItsOwn(t *testing.T) {
	l, _ := newLog(t)
	appendN(t, l,
		Entry{Actor: sam, Action: "a.b", Result: OK, Params: json.RawMessage(`{"x":"<&>"}`)},
		Entry{Actor: sam, Action: "a.c", Result: Refused, Error: &Error{Code: "forbidden", Message: "No."}},
		Entry{Actor: sam, Action: "a.d", Result: Failed},
	)
	var buf bytes.Buffer
	if err := l.Export(t.Context(), &buf); err != nil {
		t.Fatal(err)
	}
	hashRE := regexp.MustCompile(`,"hash":"(sha256:[0-9a-f]{64})"}$`)
	prev, n := ZeroHash, 0
	sc := bufio.NewScanner(&buf)
	for sc.Scan() {
		line := sc.Text()
		m := hashRE.FindStringSubmatchIndex(line)
		if m == nil {
			t.Fatalf("line without a trailing hash: %s", line)
		}
		body, hash := line[:m[0]]+"}", line[m[2]:m[3]]
		var ev struct {
			PrevHash string `json:"prev_hash"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil || ev.PrevHash != prev {
			t.Fatalf("line %d: prev_hash %q, want %q (%v)", n+1, ev.PrevHash, prev, err)
		}
		if HashOf([]byte(body)) != hash {
			t.Fatalf("line %d: the hash doesn't match the line", n+1)
		}
		prev, n = hash, n+1
	}
	if n != 3 {
		t.Fatalf("%d lines", n)
	}
}

func TestVerifyFindsTheFirstBreak(t *testing.T) {
	l, st := newLog(t)
	evs := appendN(t, l,
		Entry{Actor: sam, Action: "a.b", Result: OK},
		Entry{Actor: sam, Action: "a.c", Result: OK},
		Entry{Actor: sam, Action: "a.d", Result: OK},
		Entry{Actor: sam, Action: "a.e", Result: OK},
	)
	ctx := t.Context()
	rep, err := l.Verify(ctx)
	if err != nil || !rep.OK || rep.Checked != 4 || rep.FirstBreak != nil {
		t.Fatalf("intact chain: %+v, %v", rep, err)
	}
	empty, _ := New(openStore(t)).Verify(ctx)
	if !empty.OK || empty.Checked != 0 {
		t.Fatalf("empty chain: %+v", empty)
	}

	// The triggers refuse tampering through SQL; someone with the file can
	// drop them. Then verify notices.
	w := st.Writer()
	if _, err := w.Exec(`UPDATE audit_events SET result = 'failed' WHERE seq = 2`); err == nil {
		t.Fatal("the update trigger let a change through")
	}
	if _, err := w.Exec(`DELETE FROM audit_events WHERE seq = 2`); err == nil {
		t.Fatal("the delete trigger let a delete through")
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := w.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`DROP TRIGGER audit_events_no_update`)
	mustExec(`DROP TRIGGER audit_events_no_delete`)

	check := func(wantSeq int64, wantReason string) {
		t.Helper()
		rep, err := l.Verify(ctx)
		if err != nil || rep.OK || rep.FirstBreak == nil || rep.FirstBreak.Seq != wantSeq || !strings.Contains(rep.FirstBreak.Reason, wantReason) {
			t.Fatalf("verify = %+v (%+v), %v; want a break at %d (%s)", rep, rep.FirstBreak, err, wantSeq, wantReason)
		}
	}

	// A filter column that disagrees with the event.
	mustExec(`UPDATE audit_events SET result = 'refused' WHERE seq = 3`)
	check(3, "indexed columns")
	mustExec(`UPDATE audit_events SET result = 'ok' WHERE seq = 3`)

	// Edited contents, hash left alone.
	var body string
	st.Reader().QueryRow(`SELECT event FROM audit_events WHERE seq = 2`).Scan(&body)
	mustExec(`UPDATE audit_events SET event = ? WHERE seq = 2`, strings.Replace(body, `"a.c"`, `"a.x"`, 1))
	check(2, "hash doesn't match")

	// Contents and hash rewritten together: the next event's prev_hash gives it away.
	forged := evs[1]
	forged.Action = "a.x"
	fb, _ := Canonical(forged)
	mustExec(`UPDATE audit_events SET event = ?, hash = ?, action = 'a.x' WHERE seq = 2`, string(fb), HashOf(fb))
	check(3, "prev_hash")

	// A removed event.
	mustExec(`DELETE FROM audit_events WHERE seq = 1`)
	check(1, "missing")
}

func TestHTTP(t *testing.T) {
	l, _ := newLog(t)
	appendN(t, l, Entry{Actor: sam, Action: "a.b", Result: OK}, Entry{Actor: sam, Action: "a.c", Result: Refused})
	mux := http.NewServeMux()
	allowed := true
	l.Register(mux, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !allowed {
				http.Error(w, "no", http.StatusForbidden)
				return
			}
			h.ServeHTTP(w, r)
		})
	}, nil)
	get := func(path string) (int, string) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec.Code, rec.Body.String()
	}
	if code, body := get("/api/audit?result=refused&limit=10"); code != 200 || !strings.Contains(body, `"action":"a.c"`) || strings.Contains(body, `"a.b"`) {
		t.Fatalf("query: %d %s", code, body)
	}
	if code, body := get("/api/audit?limit=1"); code != 200 || !strings.Contains(body, `"next":"s2"`) {
		t.Fatalf("paged query: %d %s", code, body)
	}
	for _, bad := range []string{"?result=maybe", "?since=yesterday", "?limit=0", "?limit=501", "?cursor=x"} {
		if code, _ := get("/api/audit" + bad); code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d", bad, code)
		}
	}
	if code, body := get("/api/audit/export"); code != 200 || strings.Count(body, "\n") != 2 {
		t.Fatalf("export: %d %q", code, body)
	}
	if code, body := get("/api/audit/verify"); code != 200 || body != "{\"ok\":true,\"checked\":2}\n" {
		t.Fatalf("verify: %d %q", code, body)
	}
	allowed = false
	if code, _ := get("/api/audit"); code != http.StatusForbidden {
		t.Fatalf("wrap not applied: %d", code)
	}
}

// Nothing in the hub may update or delete an audit event. Tests may (to
// show that verify catches it), so they're left out.
func TestNoUpdateOrDelete(t *testing.T) {
	forbidden := regexp.MustCompile(`(?is)\b(update\s+(or\s+\w+\s+)?audit_events\b|delete\s+from\s+audit_events\b|replace\s+into\s+audit_events\b|insert\s+or\s+replace\s+into\s+audit_events\b)`)
	// The pattern must catch what it's for, or the walk below proves nothing.
	for _, s := range []string{"UPDATE audit_events SET x = 1", "delete\n from audit_events", "INSERT OR REPLACE INTO audit_events", "update or ignore audit_events"} {
		if !forbidden.MatchString(s) {
			t.Fatalf("the pattern misses %q", s)
		}
	}
	root := moduleRoot(t)
	checked := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", "dist", "web", "design", "spikes":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, "_test.go") || !(strings.HasSuffix(path, ".go") || strings.HasSuffix(path, ".sql")) {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		checked++
		if m := forbidden.Find(b); m != nil {
			t.Errorf("%s: %q: audit events are append-only", path, m)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 50 {
		t.Fatalf("only %d files checked; the walk isn't seeing the code", checked)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test")
		}
		dir = parent
	}
}
