package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"

	"dash.cha.sh/internal/node"
	"dash.cha.sh/internal/nodeproto"
)

// Stream limits.
const (
	// maxTail bounds how many past log lines one call returns.
	maxTail = 10000
	// defaultTail is the tail when the request names none.
	defaultTail = 200
	// maxLine cuts a log line; the rest is dropped with a marker.
	maxLine = 16 << 10
)

type eventsReq struct {
	// Filters are Docker's event filters: {"type":["container"],"event":["start"]}.
	Filters map[string][]string `json:"filters,omitempty"`
	// Since and Until are Unix seconds (or Docker's other forms); with Until
	// the stream ends there.
	Since string `json:"since,omitempty"`
	Until string `json:"until,omitempty"`
}

// event is what leaves the node of a Docker event: the actor's attributes
// carry every label of the container (secrets included), so only its name
// and image go.
type event struct {
	Type   string `json:"Type"`
	Action string `json:"Action"`
	Actor  struct {
		ID         string            `json:"ID"`
		Attributes map[string]string `json:"Attributes,omitempty"`
	} `json:"Actor"`
	Scope    string `json:"scope,omitempty"`
	Time     int64  `json:"time,omitempty"`
	TimeNano int64  `json:"timeNano,omitempty"`
}

var tsRE = regexp.MustCompile(`^[0-9]{1,12}(\.[0-9]{1,9})?$`)

func (m *Module) events(ctx context.Context, req json.RawMessage, emit func(json.RawMessage)) (json.RawMessage, error) {
	var r eventsReq
	if err := decode(req, &r); err != nil {
		return nil, err
	}
	for _, ts := range []string{r.Since, r.Until} {
		if ts != "" && !tsRE.MatchString(ts) {
			return nil, node.Invalid("since and until are Unix seconds")
		}
	}
	f := client.Filters{}
	for k, vs := range r.Filters {
		f.Add(k, vs...)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	res := m.cli.Events(ctx, client.EventsListOptions{Since: r.Since, Until: r.Until, Filters: f})
	n := 0
	for {
		select {
		case msg, ok := <-res.Messages:
			if !ok {
				return reply(map[string]int{"events": n})
			}
			var e event
			e.Type, e.Action, e.Scope, e.Time, e.TimeNano = string(msg.Type), string(msg.Action), msg.Scope, msg.Time, msg.TimeNano
			e.Actor.ID = msg.Actor.ID
			for _, k := range []string{"name", "image"} {
				if v, ok := msg.Actor.Attributes[k]; ok {
					if e.Actor.Attributes == nil {
						e.Actor.Attributes = map[string]string{}
					}
					e.Actor.Attributes[k] = v
				}
			}
			b, _ := json.Marshal(e)
			emit(b)
			n++
		case err, ok := <-res.Err:
			if !ok || errors.Is(err, io.EOF) {
				return reply(map[string]int{"events": n})
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, mapErr(err)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

type logsReq struct {
	ID string `json:"id"`
	// Tail is how many past lines to send: a number, or "all" (capped at
	// maxTail). Default 200.
	Tail       string `json:"tail,omitempty"`
	Since      string `json:"since,omitempty"` // Unix seconds
	Follow     bool   `json:"follow,omitempty"`
	Timestamps bool   `json:"timestamps,omitempty"`
}

// logLine is one stream item of container.logs.
type logLine struct {
	Stream string `json:"stream"` // stdout or stderr
	Line   string `json:"line"`
}

func (m *Module) logs(ctx context.Context, req json.RawMessage, emit func(json.RawMessage)) (json.RawMessage, error) {
	var r logsReq
	if err := decode(req, &r); err != nil {
		return nil, err
	}
	if !idRE.MatchString(r.ID) {
		return nil, node.Invalid("id must be a container ID or name")
	}
	tail := defaultTail
	switch r.Tail {
	case "":
	case "all":
		tail = maxTail
	default:
		n, err := strconv.Atoi(r.Tail)
		if err != nil || n < 0 {
			return nil, node.Invalid("tail is a number of lines, or all")
		}
		tail = min(n, maxTail)
	}
	if r.Since != "" && !tsRE.MatchString(r.Since) {
		return nil, node.Invalid("since is Unix seconds")
	}
	res, err := m.cli.ContainerInspect(ctx, r.ID, client.ContainerInspectOptions{})
	if err != nil {
		return nil, mapErr(err)
	}
	tty := res.Container.Config != nil && res.Container.Config.Tty
	rc, err := m.cli.ContainerLogs(ctx, res.Container.ID, client.ContainerLogsOptions{
		ShowStdout: true, ShowStderr: true, Since: r.Since, Follow: r.Follow,
		Timestamps: r.Timestamps, Tail: strconv.Itoa(tail),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	defer rc.Close()
	n := 0
	out := &lineWriter{stream: "stdout", emit: emit, n: &n}
	errw := &lineWriter{stream: "stderr", emit: emit, n: &n}
	if tty {
		_, err = io.Copy(out, rc)
	} else {
		_, err = stdcopy.StdCopy(out, errw, rc)
	}
	out.flush()
	errw.flush()
	if err != nil && ctx.Err() == nil && !errors.Is(err, io.EOF) {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "reading logs: %v", err)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return reply(map[string]int{"lines": n})
}

// lineWriter splits a log stream into lines and emits each.
type lineWriter struct {
	stream string
	emit   func(json.RawMessage)
	buf    bytes.Buffer
	n      *int
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	for {
		i := bytes.IndexByte(w.buf.Bytes(), '\n')
		if i < 0 {
			if w.buf.Len() > maxLine {
				w.send(w.buf.Next(maxLine))
			}
			return len(p), nil
		}
		w.send(w.buf.Next(i + 1))
	}
}

func (w *lineWriter) flush() {
	if w.buf.Len() > 0 {
		w.send(w.buf.Next(w.buf.Len()))
	}
}

func (w *lineWriter) send(b []byte) {
	line := strings.TrimRight(string(b), "\r\n")
	if len(line) > maxLine {
		line = line[:maxLine] + " …"
	}
	msg, _ := json.Marshal(logLine{Stream: w.stream, Line: strings.ToValidUTF8(line, "�")})
	w.emit(msg)
	*w.n++
}

// pullProgress is one stream item of container.pull.
type pullProgress struct {
	Status  string `json:"status"`
	Layer   string `json:"id,omitempty"`
	Current int64  `json:"current,omitempty"`
	Total   int64  `json:"total,omitempty"`
}

// pull pulls the image a container was created from (its tag, not its
// digest). The container keeps running the old image until it's recreated.
func (m *Module) pull(ctx context.Context, req json.RawMessage, emit func(json.RawMessage)) (json.RawMessage, error) {
	t, err := decodeTarget(req)
	if err != nil {
		return nil, err
	}
	res, err := m.writable(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	c := res.Container
	if c.Config == nil || c.Config.Image == "" {
		return nil, nodeproto.Errorf(nodeproto.CodeConflict, "%s has no image to pull", strings.TrimPrefix(c.Name, "/"))
	}
	ref := c.Config.Image
	if strings.HasPrefix(ref, "sha256:") {
		return nil, nodeproto.Errorf(nodeproto.CodeConflict, "%s was created from an image ID, not a tag, so there's nothing to pull", strings.TrimPrefix(c.Name, "/"))
	}
	before := m.imageID(ctx, ref)
	pr, err := m.cli.ImagePull(ctx, ref, client.ImagePullOptions{})
	if err != nil {
		return nil, mapErr(err)
	}
	defer pr.Close()
	for msg, err := range pr.JSONMessages(ctx) {
		if err != nil {
			return nil, mapErr(err)
		}
		if msg.Error != nil {
			return nil, nodeproto.Errorf(nodeproto.CodeFailed, "pulling %s: %s", ref, msg.Error.Message)
		}
		p := pullProgress{Status: msg.Status, Layer: msg.ID}
		if msg.Progress != nil {
			p.Current, p.Total = msg.Progress.Current, msg.Progress.Total
		}
		b, _ := json.Marshal(p)
		emit(b)
	}
	after := m.imageID(ctx, ref)
	return reply(map[string]any{"image": ref, "before": before, "after": after, "changed": after != "" && after != before, "running": c.Image})
}

func (m *Module) imageID(ctx context.Context, ref string) string {
	r, err := m.cli.ImageInspect(ctx, ref)
	if err != nil {
		return ""
	}
	return r.ID
}
