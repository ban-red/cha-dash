package qbittorrent

import (
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
	n := 0
	switch r.URL.Query().Get("filter") {
	case running:
		n = w.running
	case paused:
		n = w.stopped
	default:
		http.Error(rw, "unknown filter", http.StatusBadRequest)
		return
	}
	if lim := r.URL.Query().Get("limit"); lim == "1" {
		n = min(n, 1)
	}
	var items []string
	for i := range n {
		items = append(items, fmt.Sprintf(`{"hash":"%040d"}`, i))
	}
	rw.Header().Set("Content-Type", "application/json")
	rw.Write([]byte("[" + strings.Join(items, ",") + "]"))
}

// control pauses or resumes everything: hashes=all only.
func (w *webui) control(rw http.ResponseWriter, r *http.Request) {
	verb := strings.TrimPrefix(r.URL.Path, "/api/v2/torrents/")
	if (verb == "pause" || verb == "resume") != w.v4() {
		http.NotFound(rw, r) // each Web API knows its own names only
		return
	}
	if r.Method != http.MethodPost || r.PostFormValue("hashes") != "all" {
		http.Error(rw, "bad request", http.StatusBadRequest)
		return
	}
	w.posts = append(w.posts, verb)
	all := w.running + w.stopped
	if verb == "stop" || verb == "pause" {
		w.running, w.stopped = 0, all
	} else {
		w.running, w.stopped = all, 0
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

			_, plan := env.Succeeded(ActPause, app, nil)
			if plan.Label != "Pause all torrents in Test" || plan.Tier != action.T1 || plan.Confirm != "undo" ||
				plan.Undo == nil || plan.Undo.Action != ActResume || plan.Undo.Label != "Resume all torrents in Test" {
				t.Fatalf("plan = %+v", plan)
			}
			if r := env.Refusal(ActPause, app, nil); r.Code != "precondition" || r.Message != "No torrent in Test is running." {
				t.Fatalf("refusal = %+v", r)
			}
			env.Succeeded(ActResume, app, nil)
			if r := env.Refusal(ActResume, app, nil); r.Code != "precondition" || r.Message != "No torrent in Test is paused." {
				t.Fatalf("refusal = %+v", r)
			}
			want := []string{"stop", "start"}
			if api != "" {
				want = []string{"pause", "resume"}
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
