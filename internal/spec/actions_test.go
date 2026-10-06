package spec

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/integration/integrationtest"
)

// actionSpec is base with a list fanned out into resources, a target and
// two actions: one with a body and a path param, one whose verify never
// holds.
const actionSpec = base + `targets:
  job:
    kind: job
    pattern: '^[a-z0-9-]{1,32}$'
    read:
      path: /api/jobs/{{ target.id }}
      extract: { state: state, title: title }
    name: target.title ?? target.id
    state: target.state
actions:
  - id: widgets.job.move
    target: job
    title: Move
    tier: T2
    label: '"Move " + target.name + " to " + params.queue'
    params:
      queue: { type: string, title: Queue, required: true, enum: [fast, slow] }
      note: { type: string, title: Note, max_length: 40 }
    preconditions:
      - { when: 'target.state == "done"', message: 'target.name + " is done."' }
    request:
      method: PUT
      path: /api/jobs/{{ target.id }}/queue/{{ params.queue }}
      body: { note: 'params.note ?? ""', from: target.state }
    verify: { until: 'target.state == "moved"', message: '"not moved"', timeout: 1s }
  - id: widgets.job.stop
    target: job
    title: Stop
    tier: T1
    undo: widgets.job.start
    request: { method: DELETE, path: '/api/jobs/{{ target.id }}' }
    verify: { until: 'target.state == "stopped"', message: '"Widgets didn''t stop " + target.name + "."', timeout: 1s }
  - id: widgets.job.start
    target: job
    title: Start
    tier: T1
    request: { method: POST, path: '/api/jobs/{{ target.id }}/start' }
`

func TestActionsCompile(t *testing.T) {
	s := mustParse(t, actionSpec)
	if len(s.actions) != 3 || s.actions[0].tier != action.T2 {
		t.Fatalf("actions = %+v", s.actions)
	}
	bad := map[string]struct{ from, to string }{
		"unanchored pattern":   {`pattern: '^[a-z0-9-]{1,32}$'`, `pattern: '[a-z0-9-]+'`},
		"pattern allows slash": {`pattern: '^[a-z0-9-]{1,32}$'`, `pattern: '^[a-z0-9/-]{1,32}$'`},
		"pattern allows dots":  {`pattern: '^[a-z0-9-]{1,32}$'`, `pattern: '^[a-z0-9.-]{1,32}$'`},
		"secret in read":       {`path: /api/jobs/{{ target.id }}`, `path: /api/jobs/{{ secret.token }}`},
		"config in write":      {`path: '/api/jobs/{{ target.id }}/start'`, `path: '/api/jobs/{{ config.site }}/start'`},
		"read value in path":   {`path: '/api/jobs/{{ target.id }}/start'`, `path: '/api/jobs/{{ target.state }}/start'`},
		"query in path":        {`path: '/api/jobs/{{ target.id }}/start'`, `path: '/api/jobs/{{ target.id }}/start?x=1'`},
		"escape in path":       {`path: '/api/jobs/{{ target.id }}/start'`, `path: '/api/jobs/%2e%2e/start'`},
		"free param in path":   {`queue/{{ params.queue }}`, `queue/{{ params.note }}`},
		"unknown param":        {`queue/{{ params.queue }}`, `queue/{{ params.lane }}`},
		"GET write":            {`method: DELETE`, `method: GET`},
		"DELETE body":          {`method: PUT`, `method: DELETE`},
		"secret param":         {`note: { type: string`, `note: { type: secret`},
		"bad tier":             {`tier: T2`, `tier: T9`},
		"bad id":               {`id: widgets.job.move`, `id: widgets.other.move`},
		"unknown target": {`target: job
    title: Move`, `target: task
    title: Move`},
		"undo unknown":   {`undo: widgets.job.start`, `undo: widgets.job.nope`},
		"undo params":    {`undo: widgets.job.start`, `undo: widgets.job.move`},
		"extract id":     {`extract: { state: state, title: title }`, `extract: { id: state, title: title }`},
		"bad expression": {`until: 'target.state == "moved"'`, `until: 'target.state =='`},
		"long verify": {`timeout: 1s }
  - id: widgets.job.stop`, `timeout: 5m }
  - id: widgets.job.stop`},
	}
	for name, c := range bad {
		if !strings.Contains(actionSpec, c.from) {
			t.Fatalf("%s: %q isn't in the spec", name, c.from)
		}
		if _, err := Parse([]byte(strings.Replace(actionSpec, c.from, c.to, 1))); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestEach(t *testing.T) {
	src := strings.Replace(base, "extract: { count: data.count, items: data.items.#, missing: data.nope }",
		"extract: { count: data.count, items: data.items.#, missing: data.nope, list: data.items }", 1)
	src = strings.Replace(src, "\nmetrics:\n", `
  - id: item
    each: stats.list
    key: item.id
    part_of: app
    kind: thing
    name: item.name
metrics:
`, 1)
	// metrics can't name an each resource; key goes with each.
	for name, to := range map[string]string{
		"metric on each": strings.Replace(src, "{ resource: app, key: widgets.count", "{ resource: item, key: widgets.count", 1),
		"each sans key":  strings.Replace(src, "    key: item.id\n", "", 1),
	} {
		if _, err := Parse([]byte(to)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/info":
			w.Write([]byte(`{"info":{"version":"1.0","healthy":true}}`))
		default:
			w.Write([]byte(`{"data":{"count":3,"items":[{"id":"a","name":"Alpha"},{"id":"b/../x","name":"Bad"},{"id":"a","name":"Dup"},{"name":"No id"},{"id":"c","name":"Gamma"}]}}`))
		}
	}))
	defer srv.Close()
	h := integrationtest.New(t)
	inst := h.Connect(mustParse(t, src), map[string]any{"url": srv.URL, "site": "s"}, map[string]string{"token": "t0ken"})
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	var got []string
	for id, r := range h.Graph().Resources {
		if r.Kind == "thing" {
			got = append(got, string(id)+"="+r.Name)
		}
	}
	if strings.Join(slices.Sorted(slices.Values(got)), ",") != "widgets:i_test/item/a=Alpha,widgets:i_test/item/c=Gamma" {
		t.Fatalf("each = %v", got)
	}
	if h.Graph().Relations["widgets:i_test/item/c part_of widgets:i_test/app"] == nil {
		t.Fatal("part_of is missing")
	}
}

func TestActionsRun(t *testing.T) {
	old := verifyInterval
	verifyInterval = 20 * time.Millisecond
	t.Cleanup(func() { verifyInterval = old })

	var mu sync.Mutex
	state := map[string]string{"j-1": "queued", "j-2": "running", "j-3": "done"}
	writes := &integrationtest.Writes{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer t0ken" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		st, ok := state[r.PathValue("id")]
		mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"state":"` + st + `","title":"Job ` + r.PathValue("id") + `"}`))
	})
	mux.HandleFunc("PUT /api/jobs/{id}/queue/{q}", func(w http.ResponseWriter, r *http.Request) {}) // never moves
	mux.HandleFunc("DELETE /api/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		state[r.PathValue("id")] = "stopped"
		mu.Unlock()
	})
	srv := httptest.NewServer(writes.Wrap(mux))
	defer srv.Close()

	h := integrationtest.New(t)
	acts := h.Actions(mustParse(t, actionSpec), map[string]any{"url": srv.URL, "site": "s"}, map[string]string{"token": "t0ken"})

	// Stop: the write, the verify, and the undo the plan offers.
	p, err := acts.Plan("widgets.job.stop", "widgets:i_test/job/j-2", nil)
	if err != nil || p.Undo == nil || p.Undo.Action != "widgets.job.start" || p.Undo.Label != "Start Job j-2" || p.Label != "Stop Job j-2" {
		t.Fatalf("stop plan = %+v, %v", p, err)
	}
	acts.Succeeded("widgets.job.stop", "widgets:i_test/job/j-2", nil)
	writes.Expect(t, integrationtest.Write{Method: "DELETE", Path: "/api/jobs/j-2"})

	// Move: params validated (an enum, so it may sit in the path), the body
	// computed, the label from the expression; verify never holds.
	if ref := acts.Refused("widgets.job.move", "widgets:i_test/job/j-1", map[string]any{"queue": "../admin"}); ref.Code != "invalid" {
		t.Fatalf("a param outside the enum: %+v", ref)
	}
	p, err = acts.Plan("widgets.job.move", "widgets:i_test/job/j-1", map[string]any{"queue": "fast", "note": "hi"})
	if err != nil || p.Label != "Move Job j-1 to fast" {
		t.Fatalf("move plan = %+v, %v", p, err)
	}
	task := acts.Run("widgets.job.move", "widgets:i_test/job/j-1", map[string]any{"queue": "fast", "note": "hi"})
	if task.State != action.StateFailed || task.Error == nil || task.Error.Code != "verify_failed" || task.Error.Message != "not moved" {
		t.Fatalf("move = %s %+v", task.State, task.Error)
	}
	writes.Expect(t, integrationtest.Write{Method: "PUT", Path: "/api/jobs/j-1/queue/fast", JSON: map[string]any{"note": "hi", "from": "queued"}})

	// Preconditions refuse with the spec's sentence.
	if ref := acts.Refused("widgets.job.move", "widgets:i_test/job/j-3", map[string]any{"queue": "slow"}); ref.Code != "precondition" || ref.Message != "Job j-3 is done." {
		t.Fatalf("done job: %+v", ref)
	}
	writes.Expect(t)
}
