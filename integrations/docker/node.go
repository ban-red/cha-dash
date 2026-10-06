package docker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"dash.cha.sh/internal/integration"
)

// nodeTransport is the node:// endpoint: the integration's reads go to
// chadash-node's Docker verbs through Deps.Nodes, never to a socket. It
// answers the handful of GETs the integration sends as Docker would, so
// every read path, resource and ID is the same as over tcp:// or unix://.
// A path that isn't one of them gets 403, as a filtering proxy would
// answer: the probe then turns that feature off.
type nodeTransport struct {
	nodes integration.NodeCaller
	node  string
}

var (
	apiPrefix    = regexp.MustCompile(`^/v1\.[0-9]+`)
	containerRef = regexp.MustCompile(`^/containers/([a-zA-Z0-9][a-zA-Z0-9_.-]*)/(json|stats)$`)
)

func (t *nodeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		req.Body.Close()
	}
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return respond(req, http.StatusForbidden, "text/plain", []byte("the node transport only reads")), nil
	}
	path := apiPrefix.ReplaceAllString(req.URL.Path, "")
	q := req.URL.Query()
	ctx := req.Context()
	switch {
	case path == "/_ping":
		res, err := t.nodes.Call(ctx, t.node, "engine.version", nil, nil)
		if err != nil {
			return t.failure(req, err)
		}
		var v struct{ ApiVersion string }
		_ = json.Unmarshal(res, &v)
		resp := respond(req, http.StatusOK, "text/plain", []byte("OK"))
		resp.Header.Set("Api-Version", v.ApiVersion)
		return resp, nil
	case path == "/version":
		return t.verb(req, "engine.version", nil)
	case path == "/info":
		return t.verb(req, "engine.info", nil)
	case path == "/containers/json":
		return t.verb(req, "container.list", map[string]bool{"all": q.Get("all") == "1" || q.Get("all") == "true"})
	case containerRef.MatchString(path):
		m := containerRef.FindStringSubmatch(path)
		verb := "container.inspect"
		if m[2] == "stats" {
			verb = "container.stats"
		}
		return t.verb(req, verb, map[string]string{"id": m[1]})
	case path == "/events":
		return t.events(req)
	}
	return respond(req, http.StatusForbidden, "text/plain", []byte("not a node verb")), nil
}

func (t *nodeTransport) verb(req *http.Request, verb string, params any) (*http.Response, error) {
	res, err := t.nodes.Call(req.Context(), t.node, verb, params, nil)
	if err != nil {
		return t.failure(req, err)
	}
	return respond(req, http.StatusOK, "application/json", res), nil
}

// eventsReq mirrors the node's engine.events request.
type eventsReq struct {
	Filters map[string][]string `json:"filters,omitempty"`
	Since   string              `json:"since,omitempty"`
	Until   string              `json:"until,omitempty"`
}

// events streams the node's events as Docker's newline-delimited JSON. A
// window with an end (the probe's) is read whole, so a refusal is a 403;
// an open stream is piped as it arrives, and ends with an error the
// integration reconnects from.
func (t *nodeTransport) events(req *http.Request) (*http.Response, error) {
	q := req.URL.Query()
	r := eventsReq{Since: q.Get("since"), Until: q.Get("until")}
	if f := q.Get("filters"); f != "" {
		if err := json.Unmarshal([]byte(f), &r.Filters); err != nil {
			return respond(req, http.StatusBadRequest, "application/json", []byte(`{"message":"bad filters"}`)), nil
		}
	}
	if r.Until != "" {
		var buf bytes.Buffer
		_, err := t.nodes.Call(req.Context(), t.node, "engine.events", r, func(b json.RawMessage) {
			buf.Write(b)
			buf.WriteByte('\n')
		})
		if err != nil {
			return t.failure(req, err)
		}
		return respond(req, http.StatusOK, "application/json", buf.Bytes()), nil
	}
	pr, pw := io.Pipe()
	go func() {
		_, err := t.nodes.Call(req.Context(), t.node, "engine.events", r, func(b json.RawMessage) {
			_, _ = pw.Write(append(b, '\n'))
		})
		if err == nil {
			err = io.EOF
		}
		pw.CloseWithError(err)
	}()
	resp := respond(req, http.StatusOK, "application/json", nil)
	resp.Body = pr
	resp.ContentLength = -1
	return resp, nil
}

// failure answers a node or hub refusal as the status Docker or a proxy
// would have used, so the integration's existing handling applies:
// forbidden is a proxy's 403 (a capability not granted), not_found is
// Docker's 404, and a node that isn't connected is a 503 the runtime backs
// off from.
func (t *nodeTransport) failure(req *http.Request, err error) (*http.Response, error) {
	if req.Context().Err() != nil {
		return nil, req.Context().Err()
	}
	msg, _ := json.Marshal(map[string]string{"message": err.Error()})
	switch integration.NodeErrorCode(err) {
	case "forbidden":
		return respond(req, http.StatusForbidden, "text/plain", []byte(err.Error())), nil
	case "not_found":
		return respond(req, http.StatusNotFound, "application/json", msg), nil
	case "invalid":
		return respond(req, http.StatusBadRequest, "application/json", msg), nil
	case "not_connected", "unavailable", "busy":
		return respond(req, http.StatusServiceUnavailable, "application/json", msg), nil
	case "":
		return nil, fmt.Errorf("node %s: %w", t.node, err)
	}
	return respond(req, http.StatusBadGateway, "application/json", msg), nil
}

func respond(req *http.Request, status int, contentType string, body []byte) *http.Response {
	h := http.Header{}
	h.Set("Content-Type", contentType)
	return &http.Response{
		Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), StatusCode: status,
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Header: h, Request: req,
		Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)),
	}
}

// nodeName reads node://<name>.
func nodeName(s string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(s), "node://")
	return rest, ok
}
