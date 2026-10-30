package qbittorrent

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"dash.cha.sh/integrations/internal/act/acttest"
	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

// v4 reports whether the fake speaks Web API 2.10 and older (pause and
// resume; paused and resumed). Called with w.mu held.
func (w *webui) v4() bool { return strings.HasPrefix(w.webAPI, "2.8.") }

// torrents answers torrents/info for the filters actions use.
func (w *webui) torrents(rw http.ResponseWriter, r *http.Request) {
	running, paused := "running", "stopped"
	if w.v4() {
		running, paused = "resumed", "paused"
	}
	var want bool
	switch r.URL.Query().Get("filter") {
	case running:
		want = false
	case paused:
		want = true
	default:
		http.Error(rw, "unknown filter", http.StatusBadRequest)
		return
	}
	var items []string
	for i, p := range w.paused {
		if p == want {
			items = append(items, fmt.Sprintf(`{"hash":"%s"}`, hashOf(i)))
		}
	}
	if lim := r.URL.Query().Get("limit"); lim == "1" && len(items) > 1 {
		items = items[:1]
	}
	rw.Header().Set("Content-Type", "application/json")
	rw.Write([]byte("[" + strings.Join(items, ",") + "]"))
}

func hashOf(i int) string { return fmt.Sprintf("%040d", i) }

// control pauses or resumes hashes=all, or the hashes listed.
func (w *webui) control(rw http.ResponseWriter, r *http.Request) {
	verb := strings.TrimPrefix(r.URL.Path, "/api/v2/torrents/")
	if (verb == "pause" || verb == "resume") != w.v4() {
		http.NotFound(rw, r) // each Web API knows its own names only
		return
	}
	hashes := r.PostFormValue("hashes")
	if r.Method != http.MethodPost || hashes == "" {
		http.Error(rw, "bad request", http.StatusBadRequest)
		return
	}
	w.posts = append(w.posts, verb)
	pausing := verb == "stop" || verb == "pause"
	for i := range w.paused {
		if hashes == "all" || slices.Contains(strings.Split(hashes, "|"), hashOf(i)) {
			w.paused[i] = pausing
		}
	}
}

func TestPauseResume(t *testing.T) {
	for _, api := range []string{"", "2.8.19"} {
		t.Run("webapi "+api, func(t *testing.T) {
			w := newWebUI(t)
			w.with(func(w *webui) { w.webAPI = api })
			h := integrationtest.New(t)
			in := connect(t, h, w.URL, pass)
			if err := in.Sync(t.Context(), integrationtest.Watched); err != nil {
				t.Fatal(err)
			}
			env := acttest.New(t, h.Set.Graph, integration.ActionDefs(in.(integration.Actor))...)
			app := "qbittorrent:i_test/app"

			// One torrent (11) was paused before; the undo names the 11 the pause stopped.
			_, plan := env.Succeeded(ActPause, app, nil)
			if plan.Label != "Pause all torrents in Test" || plan.Tier != action.T1 || plan.Confirm != "undo" ||
				plan.Undo == nil || plan.Undo.Action != ActResume || plan.Undo.Label != "Resume the 11 torrents it changed in Test" {
				t.Fatalf("plan = %+v", plan)
			}
			var undo hashesParam
			if err := json.Unmarshal(plan.Undo.Params, &undo); err != nil || len(undo.Hashes) != 11 || slices.Contains(undo.Hashes, hashOf(11)) {
				t.Fatalf("undo params = %s", plan.Undo.Params)
			}
			if r := env.Refusal(ActPause, app, nil); r.Code != "precondition" || r.Message != "No torrent in Test is running." {
				t.Fatalf("refusal = %+v", r)
			}
			// Undo resumes those, and leaves the one paused before it alone.
			_, back := env.Succeeded(ActResume, app, plan.Undo.Params)
			if back.Label != "Resume 11 torrents in Test" {
				t.Fatalf("undo plan = %+v", back)
			}
			w.with(func(w *webui) {
				if !w.paused[11] || slices.Contains(w.paused[:11], true) {
					t.Errorf("after undo, paused = %v", w.paused)
				}
			})
			if r := env.Refusal(ActResume, app, plan.Undo.Params); r.Code != "precondition" || r.Message != "No torrent of those asked for in Test is paused." {
				t.Fatalf("refusal = %+v", r)
			}
			env.Succeeded(ActResume, app, nil)
			if r := env.Refusal(ActResume, app, nil); r.Code != "precondition" || r.Message != "No torrent in Test is paused." {
				t.Fatalf("refusal = %+v", r)
			}
			want := []string{"stop", "start", "start"}
			if api != "" {
				want = []string{"pause", "resume", "resume"}
			}
			w.with(func(w *webui) {
				if !slices.Equal(w.posts, want) {
					t.Errorf("posts = %v, want %v", w.posts, want)
				}
			})
			if r := env.Refusal(ActPause, "qbittorrent:i_other/app", nil); r.Code != "not_found" {
				t.Fatalf("another instance's app: %+v", r)
			}
			// The audit records carry no credential.
			for _, e := range env.Events() {
				if strings.Contains(string(e.Params), pass) || e.Target == "" {
					t.Fatalf("event = %+v", e)
				}
			}
		})
	}
}

// A pause that doesn't take shows as a failed check, not a success.
func TestPauseVerifies(t *testing.T) {
	w := newWebUI(t)
	h := integrationtest.New(t)
	in := connect(t, h, w.URL, pass).(*instance)
	defs := integration.ActionDefs(in)
	var pause action.Def
	for _, d := range defs {
		if d.ID == ActPause {
			pause = d
		}
	}
	ctx, cancel := acttest.Short(t)
	defer cancel()
	err := pause.Verify(ctx, action.Request{Action: ActPause, Target: action.Target{ID: "qbittorrent:i_test/app", Name: "Test"}}, nil)
	if err == nil || err.Error() != "a torrent is still running" {
		t.Fatalf("verify = %v", err)
	}
}

// A torrent that changes state between planning and approving changes the
// plan, so an undo never names torrents the pause didn't stop.
func TestPauseRefusesAStalePlan(t *testing.T) {
	w := newWebUI(t)
	h := integrationtest.New(t)
	in := connect(t, h, w.URL, pass)
	env := acttest.New(t, h.Set.Graph, integration.ActionDefs(in.(integration.Actor))...)
	p, err := env.Plan(ActPause, "qbittorrent:i_test/app", nil)
	if err != nil {
		t.Fatal(err)
	}
	w.with(func(w *webui) { w.paused[0] = true })
	_, err = env.Engine.Approve(t.Context(), acttest.Owner, p.ID, action.ApproveRequest{Hash: p.Hash}, "")
	if ref, ok := errors.AsType[*action.Refusal](err); !ok || ref.Code != "plan_changed" {
		t.Fatalf("approve after a torrent paused = %v", err)
	}
	w.with(func(w *webui) {
		if len(w.posts) != 0 {
			t.Errorf("posts = %v", w.posts)
		}
	})
}
