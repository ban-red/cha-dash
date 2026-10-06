package pbs

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const (
	controlTok    = "chadash-control@pbs!control"
	controlSecret = "00000000-fake-4000-8000-pbsctl000000"
)

func init() { taskPoll = time.Millisecond }

// fakePBS serves the read fixture's datastores and prune jobs and runs
// prune-datastore as a task, from the synthetic fixtures.
type fakePBS struct {
	*httptest.Server
	writes integrationtest.Writes

	mu          sync.Mutex
	maintenance map[string]string
	jobs        []map[string]any
	tasks       map[string]int // UPID → status reads
	exit        string
	deny        bool
	n           int
}

func newFakePBS(t *testing.T) *fakePBS {
	t.Helper()
	f := &fakePBS{maintenance: map[string]string{}, tasks: map[string]int{}, exit: "OK"}
	var jobs struct {
		Data []map[string]any `json:"data"`
	}
	b, err := os.ReadFile(filepath.Join(fixtures, "jupiter", "admin", "prune.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &jobs); err != nil {
		t.Fatal(err)
	}
	f.jobs = jobs.Data
	f.Server = httptest.NewTLSServer(f.writes.Wrap(http.HandlerFunc(f.serve)))
	t.Cleanup(f.Close)
	return f
}

func (f *fakePBS) reply(w http.ResponseWriter, status int, data any, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]any{"data": data}
	if msg != "" {
		body["message"] = msg
	}
	json.NewEncoder(w).Encode(body)
}

func (f *fakePBS) serve(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	read, control := auth == "PBSAPIToken="+fixtureTok+":"+fakeSecret, auth == "PBSAPIToken="+controlTok+":"+controlSecret
	if !read && !control {
		f.reply(w, 401, nil, "authentication failed - invalid credentials")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api2/json")
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && path == "/admin/datastore":
		var out []map[string]any
		for _, s := range stores {
			row := map[string]any{"store": s, "mount-status": "nonremovable"}
			if m := f.maintenance[s]; m != "" {
				row["maintenance"] = m
			}
			out = append(out, row)
		}
		f.reply(w, 200, out, "")
	case r.Method == http.MethodGet && path == "/admin/prune":
		f.reply(w, 200, f.jobs, "")
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/prune-datastore"):
		if !control || f.deny {
			f.reply(w, 403, nil, "permission check failed")
			return
		}
		f.n++
		store := strings.TrimSuffix(strings.TrimPrefix(path, "/admin/datastore/"), "/prune-datastore")
		upid := fmt.Sprintf(`UPID:jupiter:%08X:05F5E400:00000000:6AC0AF00:prune:%s:%s:`, 0x3000+f.n, store, controlTok)
		f.tasks[upid] = 0
		f.reply(w, 200, upid, "")
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/nodes/localhost/tasks/"):
		rest := strings.TrimPrefix(path, "/nodes/localhost/tasks/")
		i := strings.LastIndexByte(rest, '/')
		upid, sub := rest[:i], rest[i+1:]
		n, ok := f.tasks[upid]
		if !ok {
			f.reply(w, 404, nil, "no such task")
			return
		}
		if sub == "log" {
			var lines struct {
				Data []map[string]any `json:"data"`
			}
			b, _ := os.ReadFile(filepath.Join(fixtures, "actions", "prune-log.json"))
			json.Unmarshal(b, &lines)
			f.reply(w, 200, lines.Data, "")
			return
		}
		f.tasks[upid] = n + 1
		if n == 0 {
			f.reply(w, 200, map[string]any{"status": "running"}, "")
			return
		}
		f.reply(w, 200, map[string]any{"status": "stopped", "exitstatus": f.exit}, "")
	default:
		f.reply(w, 404, nil, "Path not found.")
	}
}

func pruneActions(t *testing.T, f *fakePBS, control bool) (*integrationtest.Harness, *integrationtest.Actions) {
	t.Helper()
	h := integrationtest.New(t)
	cfg := map[string]any{"url": f.URL, "tls": map[string]any{"mode": "insecure"}, "token_id": fixtureTok}
	secrets := map[string]string{"token_secret": fakeSecret}
	if control {
		cfg["control_token_id"] = controlTok
		secrets["control_token_secret"] = controlSecret
	}
	return h, h.Actions(Integration{}, cfg, secrets)
}

func storeTarget(s string) string { return "pbs:i_test/datastore/" + s }

func TestPrunePerPolicy(t *testing.T) {
	f := newFakePBS(t)
	_, acts := pruneActions(t, f, true)
	if got := acts.IDs(); !slices.Equal(got, []string{actPrune}) {
		t.Fatalf("ids = %v", got)
	}
	p, err := acts.Plan(actPrune, storeTarget("mars"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Tier != action.T2 || p.Confirm != "hold" || p.Label != "Prune mars per policy" || len(p.Steps) != 2 {
		t.Fatalf("plan = %+v", p)
	}
	task, err := acts.Approve(p)
	if err != nil {
		t.Fatal(err)
	}
	done := acts.Wait(task.ID)
	if done.State != action.StateSucceeded {
		t.Fatalf("task = %+v", done)
	}
	// The disabled job's policy, as the form fields prune-datastore takes.
	f.writes.Expect(t, integrationtest.Write{Method: "POST", Path: "/api2/json/admin/datastore/mars/prune-datastore",
		Form: url.Values{"keep-daily": {"7"}, "keep-weekly": {"4"}, "keep-monthly": {"6"}}})
	if !slices.Contains(done.Log, "remove vm/100/2026-08-30T03:00:02Z") || !strings.Contains(done.Step, "prune-mars") {
		t.Fatalf("task = %+v", done)
	}
	if ev := acts.LastEvent(); ev.Action != actPrune || ev.Target != storeTarget("mars") || ev.Tier != "T2" || ev.Result != "ok" {
		t.Fatalf("event = %+v", ev)
	}
}

func TestPruneRefusals(t *testing.T) {
	f := newFakePBS(t)
	h, acts := pruneActions(t, f, true)
	for _, c := range []struct {
		store  string
		params any
		code   string
		want   string
	}{
		{"mini-g5", nil, "precondition", "mini-g5 has no prune job, so there's no retention policy to prune by. Add one in Proxmox Backup Server first."},
		{"mars", map[string]any{"job": "prune-other"}, "precondition", "mars has no prune job called prune-other."},
		{"nope", nil, "not_found", "That target doesn't exist, or this action doesn't apply to it."},
	} {
		ref := acts.Refused(actPrune, storeTarget(c.store), c.params)
		if ref.Code != c.code || ref.Message != c.want {
			t.Errorf("%s: %+v", c.store, ref)
		}
	}
	f.mu.Lock()
	f.maintenance["store-2"] = "type=read-only,message=moving disks"
	f.mu.Unlock()
	if ref := acts.Refused(actPrune, storeTarget("store-2"), nil); ref.Message != "store-2 is in maintenance (read-only), so it can't be pruned now." {
		t.Errorf("maintenance: %+v", ref)
	}

	// Without a control token: unavailable, naming the instance.
	_, ro := pruneActions(t, f, false)
	ref := ro.Refused(actPrune, storeTarget("mars"), nil)
	if ref.Code != "unavailable" || ref.Recipe != h.ID || !strings.Contains(ref.Message, "Add a control token to Test") {
		t.Fatalf("refusal = %+v", ref)
	}

	// The token lacks DatastoreAdmin; the task fails with the recipe's way in.
	f.mu.Lock()
	f.deny = true
	f.mu.Unlock()
	done := acts.Run(actPrune, storeTarget("mars"), nil)
	if done.State != action.StateFailed || done.Error.Code != "forbidden" || !strings.Contains(done.Error.Message, "DatastoreAdmin on /datastore/mars") {
		t.Fatalf("task = %+v", done)
	}
	if ev := acts.LastEvent(); ev.Result != "failed" {
		t.Fatalf("event = %+v", ev)
	}
	if len(f.writes.All()) != 1 {
		t.Fatalf("writes = %+v", f.writes.All())
	}
}

// TestFillingSuggestsPrune: the filling incident suggests the engine's
// prune on the datastore whose prune job is disabled, with the Demo Home's
// ID and label.
func TestFillingSuggestsPrune(t *testing.T) {
	_, h, in := connect(t)
	if err := in.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	inc, ok := h.Incident("filling.mars")
	if !ok || inc.Suggested.ID != actPrune || inc.Suggested.Label != "Prune per policy" || inc.Suggested.Tier != "T2" ||
		inc.Suggested.Target != storeTarget("mars") || len(inc.Alternatives) != 1 || inc.Alternatives[0].ID != "link.open" {
		t.Fatalf("incident = %+v", inc)
	}
}

func TestPruneControlRecipe(t *testing.T) {
	r, ok := integration.Default.Recipe(ID)
	if !ok || r.Control == nil || r.Control.Access != integration.RecipeScoped || r.Control.Fills["control_token_id"] != "{{user}}!{{token}}" {
		t.Fatalf("recipe = %+v", r)
	}
}
