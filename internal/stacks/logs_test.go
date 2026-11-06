package stacks

import (
	"encoding/json"
	"strings"
	"testing"

	"dash.cha.sh/internal/action"
)

// A stack's log interleaves its containers', each line tagged with its
// service, through the node's container.logs; a stack with no containers
// answers 404; a node that refuses docker.logs ends the stream with its
// sentence.
func TestStackLogs(t *testing.T) {
	h := newHub(t)
	plain := strings.Replace(good, "${secret:db_password}", "plain", 1)
	plain = strings.Replace(plain, "x-chadash:", "  db:\n    image: postgres:17\nx-chadash:", 1)
	h.saveDraft("media", plain)
	if _, task := h.run(ActionDeploy, Target("local", "media"), ""); task.State != action.StateSucceeded {
		t.Fatalf("deploy: %+v", task.Error)
	}
	var asked []string
	refuse := false
	h.caller.stream = func(_, verb string, req json.RawMessage, progress func(json.RawMessage)) (json.RawMessage, bool, error) {
		if verb != "container.logs" {
			return nil, false, nil
		}
		var p struct {
			ID         string `json:"id"`
			Tail       string `json:"tail"`
			Timestamps bool   `json:"timestamps"`
		}
		_ = json.Unmarshal(req, &p)
		h.caller.mu.Lock()
		asked = append(asked, p.ID+" "+p.Tail+" "+map[bool]string{true: "ts"}[p.Timestamps])
		h.caller.mu.Unlock()
		if refuse {
			return nil, true, errString("this node's policy doesn't allow docker.logs")
		}
		progress(json.RawMessage(`{"stream":"stdout","line":"2026-11-05T12:00:00Z hello from ` + p.ID + `"}`))
		return json.RawMessage(`{"lines":1}`), true, nil
	}
	w := h.do("GET", "/api/stacks/local/media/logs?tail=10", nil)
	body := w.Body.String()
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") ||
		!strings.Contains(body, `"line":"hello from media-web-1","at":"2026-11-05T12:00:00Z","service":"web"`) ||
		!strings.Contains(body, `"service":"db"`) || !strings.HasSuffix(body, "event: end\ndata: {\"reason\":\"done\"}\n\n") {
		t.Fatalf("stack logs: %d %q", w.Code, body)
	}
	if len(asked) != 2 || !strings.HasSuffix(asked[0], " 10 ts") {
		t.Errorf("asked %q", asked)
	}
	refuse = true
	if w := h.do("GET", "/api/stacks/local/media/logs", nil); w.Code == 200 && !strings.Contains(w.Body.String(), "doesn't allow docker.logs") {
		t.Errorf("a refusing node: %d %s", w.Code, w.Body)
	}
	if w := h.do("GET", "/api/stacks/local/nothing/logs", nil); w.Code != 404 {
		t.Errorf("no such stack: %d %s", w.Code, w.Body)
	}
	if w := h.do("GET", "/api/stacks/local/media/logs?tail=x", nil); w.Code != 400 {
		t.Errorf("bad tail: %d", w.Code)
	}
}

type errString string

func (e errString) Error() string { return string(e) }
