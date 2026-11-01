// Package mcp is the hub's Model Context Protocol endpoint, mcp/v0
// (ADR-0017, amended 2026-11-01; docs/agents.md): POST /mcp, streamable
// HTTP, JSON-RPC 2.0, one message per request, answered with
// application/json. No stdio, no server-sent events (GET /mcp answers 405,
// as the transport allows), no sessions.
//
// It answers only while Agents is on (404 otherwise) and only agent tokens
// (authz.Guard's AgentToken access). An agent reads the Home Graph, open
// incidents and cause hints as its token's user sees them, narrowed by the
// token's read scopes, and plans and proposes actions through the action
// engine as an actor of kind agent: every proposal waits for a person.
//
// Methods: initialize, ping, tools/list, tools/call, resources/list,
// resources/templates/list, resources/read; notifications are accepted and
// ignored. Tools: search, get_resource, plan, propose, get_proposal. The
// input schemas of plan, propose and get_resource are generated from the
// routes they stand for (internal/apidoc.InputSchema), so they can't drift.
//
// Security, beside the token: Origin must be absent (a non-browser client),
// trusted, or the request's own address (originOK: DNS rebinding); a protocol version
// header must name a version this server speaks; bodies are bounded; and
// graph text comes back marked untrusted (view.go).
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/boards"
	"dash.cha.sh/internal/buildinfo"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/msg"
	"dash.cha.sh/internal/topics"
)

// Versions are the MCP protocol versions this server speaks, newest first.
// An initialize asking for another gets Versions[0].
var Versions = []string{"2025-06-18", "2025-03-26", "2025-11-25"}

// MaxBody bounds a request body.
const MaxBody = 256 << 10

// Options configure a Server.
type Options struct {
	Engine *action.Engine
	Topics *topics.Set
	// Boards resolves lens= narrowing (a board's scope). Nil: a lens
	// matches nothing.
	Boards *boards.Service
	// InboxView is the inbox as a role sees it (server.InboxView). Nil:
	// the whole inbox, approvals left out.
	InboxView func(ctx context.Context, role string) topics.InboxView
	// AgentsOn is the Agents switch. Nil: off, and the endpoint answers 404.
	AgentsOn func(ctx context.Context) (bool, error)
	// Origins are trusted origins besides the request's own
	// (CHADASH_ALLOWED_ORIGINS, CHADASH_PUBLIC_URL), scheme://host[:port].
	Origins []string
	Log     *slog.Logger
}

// Server serves POST /mcp.
type Server struct {
	engine    *action.Engine
	topics    *topics.Set
	boards    *boards.Service
	inboxView func(ctx context.Context, role string) topics.InboxView
	agentsOn  func(ctx context.Context) (bool, error)
	origins   []string
	log       *slog.Logger
	// perToken bounds requests per agent token: every call can plan,
	// audit a refusal and ask the lab, whatever the propose limits say.
	perToken *httpx.Limiter
	tools    []tool
}

// An agent token may send RequestBurst requests at once, then one per
// RequestEvery.
const (
	RequestBurst = 60
	RequestEvery = time.Second
)

// New returns a Server.
func New(o Options) *Server {
	s := &Server{engine: o.Engine, topics: o.Topics, boards: o.Boards, inboxView: o.InboxView, agentsOn: o.AgentsOn, log: o.Log,
		perToken: httpx.NewLimiter(RequestBurst, RequestEvery)}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	for _, origin := range o.Origins {
		if u, err := url.Parse(strings.TrimRight(origin, "/")); err == nil && u.Host != "" {
			s.origins = append(s.origins, strings.ToLower(u.Scheme+"://"+u.Host))
		}
	}
	s.tools = s.toolSet()
	return s
}

// Register mounts POST /mcp behind wrap. Guard has admitted only an agent
// token by then; the handler checks again.
func (s *Server) Register(mux *http.ServeMux, wrap func(http.Handler) http.Handler) {
	mux.Handle("POST /mcp", wrap(http.HandlerFunc(s.serveHTTP)))
	// Any other method: no stream to open (GET), no session to end
	// (DELETE). 405, as the transport allows, and never the web app.
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", http.MethodPost)
		httpx.Say(w, http.StatusMethodNotAllowed, "method_not_allowed", msg.New("agents.mcp_post_only"))
	})
}

// JSON-RPC 2.0 error codes.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeNoMethod       = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
)

// message is one JSON-RPC message in.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

// rpcError is a JSON-RPC error.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return e.Message }

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	on := false
	if s.agentsOn != nil {
		var err error
		if on, err = s.agentsOn(ctx); err != nil {
			s.log.Error("mcp: reading the Agents setting failed", "err", err)
			on = false
		}
	}
	if !on {
		// It doesn't answer while Agents is off (ADR-0017).
		httpx.Say(w, http.StatusNotFound, "agents_off", msg.New("agents.off"))
		return
	}
	id, ok := auth.FromContext(ctx)
	if !ok || id.Token == nil || id.Token.Kind != auth.KindAgent {
		httpx.Say(w, http.StatusForbidden, "agent_token_only", msg.New("agents.not_agent_token"))
		return
	}
	if ok, wait := s.perToken.Allow(id.Token.ID); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		httpx.Say(w, http.StatusTooManyRequests, "rate_limited", msg.New("agents.mcp_rate_limited"))
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && !s.originOK(r, origin) {
		httpx.Say(w, http.StatusForbidden, "forbidden", msg.New("agents.bad_origin", "origin", clip(origin, 200)))
		return
	}
	if v := r.Header.Get("MCP-Protocol-Version"); v != "" && !slices.Contains(Versions, v) {
		httpx.Say(w, http.StatusBadRequest, "invalid", msg.New("agents.bad_protocol", "version", clip(v, 40)))
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		httpx.Say(w, http.StatusUnsupportedMediaType, "invalid", msg.New("request.not_json"))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBody))
	if err != nil {
		httpx.Say(w, http.StatusRequestEntityTooLarge, "invalid", msg.New("request.too_large"))
		return
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		writeRPC(w, http.StatusBadRequest, response{JSONRPC: "2.0", ID: json.RawMessage("null"),
			Error: &rpcError{Code: codeInvalidRequest, Message: msg.T("agents.mcp_batch", nil)}})
		return
	}
	var m message
	if err := json.Unmarshal(trimmed, &m); err != nil {
		writeRPC(w, http.StatusBadRequest, response{JSONRPC: "2.0", ID: json.RawMessage("null"),
			Error: &rpcError{Code: codeParse, Message: msg.T("agents.mcp_bad_json", nil)}})
		return
	}
	if m.JSONRPC != "2.0" || (m.Method == "" && m.Result == nil && m.Error == nil) {
		writeRPC(w, http.StatusBadRequest, response{JSONRPC: "2.0", ID: orNull(m.ID),
			Error: &rpcError{Code: codeInvalidRequest, Message: msg.T("agents.mcp_bad_json", nil)}})
		return
	}
	if m.Method == "" || len(m.ID) == 0 || string(m.ID) == "null" {
		// A response or a notification (initialized, cancelled): accepted,
		// nothing to answer.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	v := viewer{role: id.User.Role, scopes: slices.Clone(id.Token.Scopes), actor: action.Actor{
		Kind: action.KindAgent, ID: id.Token.ID, Name: authz.AgentActorName(id.Token.Name, id.User.Username),
		Role: id.User.Role, Via: "mcp", IP: id.IP, User: id.User.ID, UserName: id.User.Username,
	}}
	result, rerr := s.dispatch(ctx, v, m.Method, m.Params)
	resp := response{JSONRPC: "2.0", ID: m.ID}
	if rerr != nil {
		resp.Error = rerr
	} else {
		resp.Result = result
	}
	writeRPC(w, http.StatusOK, resp)
}

func writeRPC(w http.ResponseWriter, status int, resp response) {
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, status, resp)
}

func orNull(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}

// originOK admits a browser's request from a trusted origin
// (CHADASH_ALLOWED_ORIGINS, CHADASH_PUBLIC_URL), or from the request's own
// Host when that's an address or localhost. A hostname that's neither
// configured nor an address is refused even when Origin and Host agree:
// that's what DNS rebinding looks like (a name the attacker controls,
// pointed at the hub). Clients outside a browser send no Origin.
func (s *Server) originOK(r *http.Request, origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if slices.Contains(s.origins, strings.ToLower(u.Scheme+"://"+u.Host)) {
		return true
	}
	if !strings.EqualFold(u.Host, r.Host) {
		return false
	}
	host := u.Hostname()
	if _, err := netip.ParseAddr(host); err == nil {
		return true
	}
	return strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost")
}

func (s *Server) dispatch(ctx context.Context, v viewer, method string, params json.RawMessage) (any, *rpcError) {
	switch method {
	case "initialize":
		return s.initialize(params), nil
	case "ping":
		return struct{}{}, nil
	case "tools/list":
		return map[string]any{"tools": s.toolList()}, nil
	case "tools/call":
		return s.callTool(ctx, v, params)
	case "resources/list":
		return map[string]any{"resources": s.resourceList()}, nil
	case "resources/templates/list":
		return map[string]any{"resourceTemplates": s.resourceTemplates()}, nil
	case "resources/read":
		return s.readResource(ctx, v, params)
	case "prompts/list":
		return map[string]any{"prompts": []any{}}, nil
	}
	return nil, &rpcError{Code: codeNoMethod, Message: msg.New("agents.mcp_no_method", "method", clip(method, 100)).Text()}
}

func (s *Server) initialize(params json.RawMessage) any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &p)
	version := Versions[0]
	if slices.Contains(Versions, p.ProtocolVersion) {
		version = p.ProtocolVersion
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities": map[string]any{
			"tools":     map[string]any{"listChanged": false},
			"resources": map[string]any{"subscribe": false, "listChanged": false},
		},
		"serverInfo":   map[string]any{"name": "chadash", "title": "Cha Dash", "version": buildinfo.Version},
		"instructions": msg.T("agents.mcp_instructions", nil),
	}
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// paramsErr is an invalid-params error naming the method and why.
func paramsErr(method string, err error) *rpcError {
	reason := "malformed"
	if err != nil {
		var se *json.SyntaxError
		var te *json.UnmarshalTypeError
		switch {
		case errors.As(err, &se):
			reason = "malformed JSON"
		case errors.As(err, &te):
			reason = te.Field + " has the wrong type"
		default:
			reason = clip(err.Error(), 200)
		}
	}
	return &rpcError{Code: codeInvalidParams, Message: msg.New("agents.mcp_bad_params", "method", method, "reason", reason).Text()}
}
