package pbs

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration/integrationtest"
)

// Verify now: T1, the whole datastore or one group, and the result is
// recorded for the datastore.
func TestVerifyNow(t *testing.T) {
	t.Cleanup(func() { verified.Clear() })
	f := newFakePBS(t)
	h, acts := pruneActions(t, f, true)
	p, err := acts.Plan(ActVerify, storeTarget("mars"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Tier != action.T1 || p.Label != "Verify mars now" || len(p.Steps) != 2 || !strings.Contains(p.Steps[0].Label, "every snapshot on mars") {
		t.Fatalf("plan = %+v", p)
	}
	task, err := acts.Approve(p)
	if err != nil {
		t.Fatal(err)
	}
	if done := acts.Wait(task.ID); done.State != action.StateSucceeded {
		t.Fatalf("task = %+v", done)
	}
	f.writes.Expect(t, integrationtest.Write{Method: "POST", Path: "/api2/json/admin/datastore/mars/verify", Form: url.Values{}})
	v, ok := verified.Load(verifiedKey(h.ID, "mars"))
	if res, _ := v.(verifyResult); !ok || res.State != "OK" || res.Group != "" {
		t.Fatalf("recorded = %+v", v)
	}
	if ev := acts.LastEvent(); ev.Action != ActVerify || ev.Tier != "T1" || ev.Result != "ok" {
		t.Fatalf("event = %+v", ev)
	}

	// One snapshot group, in a namespace.
	done := acts.Run(ActVerify, storeTarget("store-2"), map[string]any{"group": "vm/100", "ns": "pve"})
	if done.State != action.StateSucceeded {
		t.Fatalf("group task = %+v", done)
	}
	f.writes.Expect(t, integrationtest.Write{Method: "POST", Path: "/api2/json/admin/datastore/store-2/verify",
		Form: url.Values{"backup-type": {"vm"}, "backup-id": {"100"}, "ns": {"pve"}}})
	if res, _ := verified.Load(verifiedKey(h.ID, "store-2")); res.(verifyResult).Group != "vm/100" {
		t.Fatalf("group recorded = %+v", res)
	}

	// A bad group is refused before anything is sent.
	if ref := acts.Refused(ActVerify, storeTarget("mars"), map[string]any{"group": "../etc"}); ref.Code != "invalid" {
		t.Fatalf("bad group: %+v", ref)
	}
	// A failed verify fails the task and is recorded.
	f.mu.Lock()
	f.exit = "verification failed - please check the log for details"
	f.mu.Unlock()
	done = acts.Run(ActVerify, storeTarget("mars"), nil)
	if done.State != action.StateFailed || done.Error == nil || !strings.Contains(done.Error.Message, "couldn't verify mars") {
		t.Fatalf("failed task = %+v", done)
	}
	if res, _ := verified.Load(verifiedKey(h.ID, "mars")); okState(res.(verifyResult).State) {
		t.Fatalf("failure recorded = %+v", res)
	}
}

func TestVerifyRefusals(t *testing.T) {
	f := newFakePBS(t)
	h, acts := pruneActions(t, f, true)
	// Read-only maintenance still reads; offline doesn't.
	f.mu.Lock()
	f.maintenance["store-2"] = "type=read-only"
	f.maintenance["mars"] = "type=offline,message=new disks"
	f.lacks["/datastore/mini-g5"] = []string{"Datastore.Verify"}
	f.mu.Unlock()
	if _, err := acts.Plan(ActVerify, storeTarget("store-2"), nil); err != nil {
		t.Fatalf("read-only: %v", err)
	}
	if ref := acts.Refused(ActVerify, storeTarget("mars"), nil); ref.Code != "precondition" || ref.Message != "mars is in maintenance (offline), so it can't be verified now." {
		t.Fatalf("offline: %+v", ref)
	}
	want := "The control token can't verify mini-g5: it lacks Datastore.Verify on /datastore/mini-g5. Give it the DatastoreAdmin role there, as the control recipe does."
	if ref := acts.Refused(ActVerify, storeTarget("mini-g5"), nil); ref.Code != "unavailable" || ref.Message != want || ref.Recipe != h.ID {
		t.Fatalf("lacks: %+v", ref)
	}
	_, ro := pruneActions(t, f, false)
	if ref := ro.Refused(ActVerify, storeTarget("store-2"), nil); ref.Code != "unavailable" || !strings.Contains(ref.Message, "prune and verify") {
		t.Fatalf("no control token: %+v", ref)
	}
	if len(f.writes.All()) != 0 {
		t.Fatalf("writes = %+v", f.writes.All())
	}
}

// A verify run from the hub shows on its datastore when it's newer than
// the verify jobs', a failed one in the datastore's reason.
func TestVerifyNowShowsOnTheDatastore(t *testing.T) {
	t.Cleanup(func() { verified.Clear() })
	_, h, in := connect(t)
	at := h.Now().Add(-time.Minute).UTC()
	verified.Store(verifiedKey(in.key, "mars"), verifyResult{At: at, State: "OK", Group: "vm/100"})
	verified.Store(verifiedKey(in.key, "store-2"), verifyResult{At: at, State: "verification failed - please check the log"})
	syncOK(t, in, integrationtest.Watched)
	m := res(t, h, "datastore/mars")
	if m.Attrs["verify_now"] != true || m.Attrs["verify_state"] != "OK" || m.Attrs["verify_group"] != "vm/100" {
		t.Fatalf("mars = %+v", m.Attrs)
	}
	s := res(t, h, "datastore/store-2")
	if s.Attrs["verify_now"] != true || s.Health != graph.HealthAttention || !strings.Contains(s.Attrs["health.reason"].(string), "verification failed") {
		t.Fatalf("store-2 = %+v %s", s.Attrs, s.Health)
	}
	// One older than the verify job's stays out of the way.
	verified.Store(verifiedKey(in.key, "store-2"), verifyResult{At: time.Unix(0, 0), State: "OK"})
	syncOK(t, in, integrationtest.Watched)
	if s := res(t, h, "datastore/store-2"); s.Attrs["verify_now"] != nil || okState(s.Attrs["verify_state"].(string)) {
		t.Fatalf("old result shown: %+v", s.Attrs)
	}
}

// The last verify run from the hub is kept in the instance's state, so it
// shows on its datastore again after a restart.
func TestVerifyResultSurvivesARestart(t *testing.T) {
	t.Cleanup(func() { verified.Clear() })
	srv, h, in := connect(t)
	in.recordVerify(t.Context(), "mars", "vm/100", "OK")
	in.recordVerify(t.Context(), "store-2", "", "verification failed - please check the log")
	raw, err := h.State.Load(t.Context(), verifyStateKey)
	if err != nil || !strings.Contains(string(raw), `"v":1`) || !strings.Contains(string(raw), `"mars":{`) || !strings.Contains(string(raw), `"group":"vm/100"`) {
		t.Fatalf("state = %s, %v", raw, err)
	}

	// A restart: nothing in memory, a new instance on the same state.
	verified.Clear()
	again := h.Connect(Integration{}, map[string]any{"url": srv.URL, "tls": map[string]any{"mode": "tofu"}, "token_id": fixtureTok},
		map[string]string{"token_secret": fakeSecret}).(*instance)
	syncOK(t, again, integrationtest.Watched)
	if m := res(t, h, "datastore/mars"); m.Attrs["verify_now"] != true || m.Attrs["verify_state"] != "OK" || m.Attrs["verify_group"] != "vm/100" {
		t.Fatalf("mars = %+v", m.Attrs)
	}
	if s := res(t, h, "datastore/store-2"); s.Attrs["verify_now"] != true || !strings.Contains(s.Attrs["health.reason"].(string), "verification failed") {
		t.Fatalf("store-2 = %+v", s.Attrs)
	}

	// An unreadable state is left alone, and nothing shows from it.
	verified.Clear()
	if err := h.State.Save(t.Context(), verifyStateKey, []byte(`{"v":99}`)); err != nil {
		t.Fatal(err)
	}
	third := h.Connect(Integration{}, map[string]any{"url": srv.URL, "tls": map[string]any{"mode": "tofu"}, "token_id": fixtureTok},
		map[string]string{"token_secret": fakeSecret}).(*instance)
	syncOK(t, third, integrationtest.Watched)
	if m := res(t, h, "datastore/mars"); m.Attrs["verify_now"] != nil {
		t.Fatalf("mars from an unreadable state = %+v", m.Attrs)
	}
}
