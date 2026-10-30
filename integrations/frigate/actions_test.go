package frigate

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/integration/integrationtest"
)

// withControl is the authenticated port with a viewer that reads and an
// admin that acts.
func withControl(f *fake) (map[string]any, map[string]string) {
	return map[string]any{"url": f.URL, "username": "dash", "control_username": "admin"},
		map[string]string{"password": "viewer-pass", "control_password": "admin-pass"}
}

// patchConfig serves the fixture config with edit applied to one camera.
func patchConfig(t *testing.T, f *fake, cam string, edit func(c map[string]any)) {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal(f.body("/api/config", "config.json"), &cfg); err != nil {
		t.Fatal(err)
	}
	edit(cfg["cameras"].(map[string]any)[cam].(map[string]any))
	b, _ := json.Marshal(cfg)
	f.set("/api/config", b)
}

func setFrameFor(cam, topic, payload string) string {
	b, _ := json.Marshal(map[string]any{"topic": cam + "/" + topic + "/set", "payload": payload, "retain": false})
	return string(b)
}

func TestActions(t *testing.T) {
	f := newFake(t, true)
	h := integrationtest.New(t)
	cfg, secrets := withControl(f)
	acts := h.Actions(Integration{}, cfg, secrets)
	ids := acts.IDs()
	for _, id := range []string{ActDetectOn, ActDetectOff, ActRecordOn, ActRecordOff, ActEnabledOn, ActEnabledOff,
		ActSnapshotsOn, ActSnapshotsOff, ActAlertsOn, ActAlertsOff} {
		if !slices.Contains(ids, id) {
			t.Errorf("%s isn't registered", id)
		}
	}
	const drive = "frigate:i_test/camera/driveway"

	// Everything is on in the fixture config.
	if ref := acts.Refused(ActDetectOn, drive, nil); ref.Code != "precondition" || ref.Message != "Detection is already on for Driveway." {
		t.Fatalf("detect on = %+v", ref)
	}
	if ref := acts.Refused(ActEnabledOn, drive, nil); ref.Message != "Driveway is already on." {
		t.Fatalf("enabled on = %+v", ref)
	}

	p, err := acts.Plan(ActDetectOff, drive, nil)
	if err != nil || p.Tier != action.T1 || p.Label != "Turn off detection for Driveway" || p.Undo == nil || p.Undo.Action != ActDetectOn ||
		p.Undo.Label != "Turn on detection for Driveway" {
		t.Fatalf("plan = %+v, %v", p, err)
	}
	task := acts.Succeeded(ActDetectOff, drive, nil)
	if task.Undo == nil || task.Undo.Action != ActDetectOn {
		t.Fatalf("task undo = %+v", task.Undo)
	}
	if got, want := f.sent(), []string{setFrameFor("driveway", "detect", "OFF")}; !slices.Equal(got, want) {
		t.Fatalf("sent %q, want %q", got, want)
	}

	// Frigate's config now says detection is off.
	patchConfig(t, f, "driveway", func(c map[string]any) { c["detect"].(map[string]any)["enabled"] = false })
	if ref := acts.Refused(ActDetectOff, drive, nil); ref.Message != "Detection is already off for Driveway." {
		t.Fatalf("detect off again = %+v", ref)
	}
	acts.Succeeded(ActDetectOn, drive, nil)

	// The other switches, each with its own topic.
	for _, c := range []struct{ id, topic string }{
		{ActRecordOff, "recordings"}, {ActEnabledOff, "enabled"}, {ActSnapshotsOff, "snapshots"}, {ActAlertsOff, "review_alerts"},
	} {
		acts.Succeeded(c.id, "frigate:i_test/camera/side_deck", nil)
		if got := f.sent(); got[len(got)-1] != setFrameFor("side_deck", c.topic, "OFF") {
			t.Errorf("%s sent %s", c.id, got[len(got)-1])
		}
	}

	// Nothing but the one set frame per action, ever.
	want := []string{setFrameFor("driveway", "detect", "OFF"), setFrameFor("driveway", "detect", "ON"),
		setFrameFor("side_deck", "recordings", "OFF"), setFrameFor("side_deck", "enabled", "OFF"),
		setFrameFor("side_deck", "snapshots", "OFF"), setFrameFor("side_deck", "review_alerts", "OFF")}
	if got := f.sent(); !slices.Equal(got, want) {
		t.Fatalf("sent %q, want %q", got, want)
	}

	// An unknown camera, or another instance's.
	for _, target := range []string{"frigate:i_test/camera/garage", "frigate:other/camera/driveway", "frigate:i_test/app"} {
		if p, err := acts.Plan(ActDetectOff, target, nil); err == nil {
			t.Errorf("%s: planned %+v", target, p)
		}
	}
}

func TestActionPreconditions(t *testing.T) {
	f := newFake(t, true)
	h := integrationtest.New(t)
	cfg, secrets := withControl(f)
	acts := h.Actions(Integration{}, cfg, secrets)
	const back = "frigate:i_test/camera/backyard"

	// Recording and review alerts are off in the config file: Frigate won't
	// turn them on at runtime.
	patchConfig(t, f, "backyard", func(c map[string]any) {
		c["record"].(map[string]any)["enabled"] = false
		c["record"].(map[string]any)["enabled_in_config"] = false
		alerts := c["review"].(map[string]any)["alerts"].(map[string]any)
		alerts["enabled"], alerts["enabled_in_config"] = false, false
		c["snapshots"].(map[string]any)["enabled"] = false
	})
	if ref := acts.Refused(ActRecordOn, back, nil); ref.Code != "precondition" || !strings.Contains(ref.Message, "Recording is off in Backyard's Frigate config") {
		t.Fatalf("record on = %+v", ref)
	}
	if ref := acts.Refused(ActAlertsOn, back, nil); !strings.Contains(ref.Message, "Review alerts is off in Backyard's Frigate config") {
		t.Fatalf("alerts on = %+v", ref)
	}
	if ref := acts.Refused(ActRecordOff, back, nil); ref.Message != "Recording is already off for Backyard." {
		t.Fatalf("record off = %+v", ref)
	}
	acts.Succeeded(ActSnapshotsOn, back, nil) // snapshots have no such rule

	// A whole camera needs Frigate 0.16.
	f.set("/api/version", []byte("0.15.1-abcdef0"))
	if ref := acts.Refused(ActEnabledOff, back, nil); ref.Message != "Turning a whole camera on or off needs Frigate 0.16 or later; this one runs 0.15.1." {
		t.Fatalf("enabled off on 0.15 = %+v", ref)
	}
	acts.Succeeded(ActDetectOff, back, nil)

	// A camera the read account's role can't see isn't a target.
	f.set("/api/profile", []byte(`{"username":"dash","role":"viewer","allowed_cameras":["driveway"]}`))
	if _, err := acts.Plan(ActDetectOff, back, nil); err == nil {
		t.Fatal("planned on a camera the role can't see")
	}
}

func TestActionNoAnswer(t *testing.T) {
	old := stateWait
	stateWait = 200 * time.Millisecond
	t.Cleanup(func() { stateWait = old })
	f := newFake(t, true)
	f.silent = true
	h := integrationtest.New(t)
	cfg, secrets := withControl(f)
	acts := h.Actions(Integration{}, cfg, secrets)
	task := acts.Run(ActDetectOff, "frigate:i_test/camera/driveway", nil)
	if task.State != action.StateFailed || task.Error == nil || task.Error.Code != "verify_failed" || !strings.Contains(task.Error.Message, "admin") {
		t.Fatalf("task = %+v %+v", task.State, task.Error)
	}
	if got := f.sent(); len(got) != 1 {
		t.Fatalf("sent %q", got)
	}
}

func TestActionsControlCredential(t *testing.T) {
	f := newFake(t, true)

	// No control account: every action is unavailable, naming the admin.
	h := integrationtest.New(t)
	acts := h.Actions(Integration{}, map[string]any{"url": f.URL, "username": "dash"}, map[string]string{"password": "viewer-pass"})
	ref := acts.Refused(ActDetectOff, "frigate:i_test/camera/driveway", nil)
	if ref.Code != "unavailable" || !strings.Contains(ref.Message, "admin") || ref.Recipe != "i_test" {
		t.Fatalf("without a control account = %+v", ref)
	}

	// A control account that isn't an admin.
	h = integrationtest.New(t)
	f.set("/api/profile#admin", []byte(`{"username":"admin","role":"viewer","allowed_cameras":[]}`))
	acts = h.Actions(Integration{}, map[string]any{"url": f.URL, "username": "dash", "control_username": "admin"},
		map[string]string{"password": "viewer-pass", "control_password": "admin-pass"})
	ref = acts.Refused(ActDetectOff, "frigate:i_test/camera/driveway", nil)
	if ref.Code != "unavailable" || !strings.Contains(ref.Message, "has the viewer role") || ref.Recipe != "i_test" {
		t.Fatalf("a viewer control account = %+v", ref)
	}

	// A wrong control password.
	h = integrationtest.New(t)
	acts = h.Actions(Integration{}, map[string]any{"url": f.URL, "username": "dash", "control_username": "admin"},
		map[string]string{"password": "viewer-pass", "control_password": "nope"})
	ref = acts.Refused(ActDetectOff, "frigate:i_test/camera/driveway", nil)
	if ref.Code != "unavailable" || !strings.Contains(ref.Message, "rejected") {
		t.Fatalf("a wrong control password = %+v", ref)
	}
	if strings.Contains(h.Logs(), "nope") || strings.Contains(h.Logs(), "admin-pass") {
		t.Fatal("a control password leaked into the logs")
	}
	if len(f.sent()) != 0 {
		t.Fatalf("sent %q", f.sent())
	}
}
