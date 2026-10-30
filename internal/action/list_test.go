package action

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"dash.cha.sh/internal/auth"
)

func TestHTTPCancelAndActions(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	slow := Def{ID: "proxmox.guest.backup", Title: "Back up", Targets: []string{"vm"}, Tier: T1, Cancellable: true,
		Execute: func(ctx context.Context, _ Request, _ *TaskRun) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-block:
				return nil
			}
		}}
	unavail := Def{ID: "proxmox.guest.snapshot.create", Title: "Snapshot", Targets: []string{"vm"}, Tier: T1,
		Preconditions: func(context.Context, Request) error {
			return Unavailable("Add a control token to let Cha Dash snapshot guests on io.", "i_x")
		},
		Execute: func(context.Context, Request, *TaskRun) error { return nil }}
	already := Def{ID: "proxmox.guest.start", Title: "Start", Targets: []string{"vm"}, Tier: T1,
		Preconditions: func(_ context.Context, r Request) error { return Precondition(r.Target.Name + " is already running.") },
		Execute:       func(context.Context, Request, *TaskRun) error { return nil }}
	hostOnly := Def{ID: "proxmox.node.reboot", Title: "Reboot", Targets: []string{"host"}, Tier: T2,
		Execute: func(context.Context, Request, *TaskRun) error { return nil }}
	var calls []string
	e := newEnv(t, rolePolicy{}, reboot(&calls), slow, unavail, already, hostOnly)

	mux := http.NewServeMux()
	users := map[string]auth.Identity{
		"sam": {User: auth.User{ID: owner.ID, Username: "sam", Role: "owner"}, Session: auth.Session{ID: owner.Session}},
		"kim": {User: auth.User{ID: other.ID, Username: "kim", Role: "operator"}, Session: auth.Session{ID: other.Session}},
		"lee": {User: auth.User{ID: "u_lee", Username: "lee", Role: "viewer"}, Session: auth.Session{ID: "s_3"}},
	}
	e.e.Register(mux, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := users[r.Header.Get("X-Test-User")]
			if !ok {
				http.Error(w, "sign in", http.StatusUnauthorized)
				return
			}
			h.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), id)))
		})
	}, slog.New(slog.DiscardHandler))
	do := func(user, method, path string) (int, []byte) {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewReader([]byte("{}")))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Test-User", user)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}

	// What can be planned on win-gaming: by label, the unavailable one with
	// its sentence; the already-running start and the host's reboot left out.
	code, body := do("kim", "GET", "/api/actions?target=proxmox:i_x/qemu/201")
	var offers []Offer
	if code != 200 || json.Unmarshal(body, &offers) != nil {
		t.Fatalf("actions: %d %s", code, body)
	}
	want := []Offer{
		{ID: "proxmox.guest.backup", Label: "Back up win-gaming", Tier: T1, Available: true},
		{ID: "proxmox.guest.reboot", Label: "Reboot win-gaming", Tier: T2, Available: true},
		{ID: "proxmox.guest.snapshot.create", Label: "Snapshot win-gaming", Tier: T1, Available: false,
			Unavailable: "Add a control token to let Cha Dash snapshot guests on io.", Recipe: "i_x"},
	}
	if len(offers) != len(want) {
		t.Fatalf("actions = %+v", offers)
	}
	for i := range want {
		if !reflect.DeepEqual(offers[i], want[i]) {
			t.Errorf("actions[%d] = %+v, want %+v", i, offers[i], want[i])
		}
	}
	// The viewer's policy allows none of them; an unknown target has none.
	if code, body := do("lee", "GET", "/api/actions?target=proxmox:i_x/qemu/201"); code != 200 || string(bytes.TrimSpace(body)) != "[]" {
		t.Fatalf("viewer's actions: %d %s", code, body)
	}
	if code, body := do("kim", "GET", "/api/actions?target=nope"); code != 200 || string(bytes.TrimSpace(body)) != "[]" {
		t.Fatalf("unknown target: %d %s", code, body)
	}
	if code, _ := do("kim", "GET", "/api/actions"); code != 422 {
		t.Fatalf("no target: %d", code)
	}
	if code, _ := do("nobody", "GET", "/api/actions?target=x"); code != 401 {
		t.Fatalf("no session: %d", code)
	}
	// Listing is a read: it audits nothing.
	if evs := e.events(); len(evs) != 0 {
		t.Fatalf("listing audited %+v", evs)
	}

	// Cancel: kim's own backup. lee can't see it.
	task, err := e.approve(other, e.mustPlan(other, "proxmox.guest.backup", "proxmox:i_x/qemu/201"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	cancel := "/api/tasks/" + task.ID + "/cancel"
	if code, body := do("lee", "POST", cancel); code != 404 {
		t.Fatalf("someone else's task: %d %s", code, body)
	}
	code, body = do("kim", "POST", cancel)
	var got struct{ Task Task }
	if code != 202 || json.Unmarshal(body, &got) != nil || got.Task.ID != task.ID || !got.Task.Cancellable {
		t.Fatalf("cancel: %d %s", code, body)
	}
	if done := e.wait(task.ID); done.State != StateCancelled {
		t.Fatalf("task = %+v", done)
	}
	if code, body := do("kim", "POST", cancel); code != 409 || !strings.Contains(string(body), `"finished"`) {
		t.Fatalf("cancel again: %d %s", code, body)
	}
	// The owner may cancel anyone's (tasks.all), but a reboot isn't cancellable.
	task, err = e.approve(owner, e.mustPlan(owner, "proxmox.guest.reboot", "proxmox:i_x/qemu/201"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if code, body := do("sam", "POST", "/api/tasks/"+task.ID+"/cancel"); code != 409 || !strings.Contains(string(body), `"not_cancellable"`) {
		t.Fatalf("cancel a reboot: %d %s", code, body)
	}
	e.wait(task.ID)
	cancels := 0
	for _, ev := range e.events() {
		if ev.Action == ActionCancel {
			cancels++
		}
	}
	if cancels != 4 {
		t.Fatalf("%d cancels audited, want 4", cancels)
	}
}

func TestOwnerCancelsAnyonesTask(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	slow := Def{ID: "x.slow", Tier: T1, Targets: []string{"app"}, Cancellable: true,
		Execute: func(ctx context.Context, _ Request, _ *TaskRun) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-block:
				return nil
			}
		}}
	e := newEnv(t, rolePolicy{}, slow)
	task, err := e.approve(other, e.mustPlan(other, "x.slow", "app:plex"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.e.Cancel(t.Context(), owner, task.ID); err != nil {
		t.Fatal(err)
	}
	if done := e.wait(task.ID); done.State != StateCancelled {
		t.Fatalf("task = %+v", done)
	}
}
