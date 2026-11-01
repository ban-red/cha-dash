package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/url"
	"slices"
	"strings"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/apidoc"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/msg"
)

// tool is one MCP tool: its listing and what calling it does.
type tool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations"`
	call        func(ctx context.Context, v viewer, args json.RawMessage) (any, error)
}

// The routes the tools stand for: their input schemas are generated from
// these rows (apidoc.InputSchema), so a tool can't drift from its route.
const (
	planRoute     = "POST /api/actions/plan"
	resourceRoute = "GET /api/causes"
)

// fromRoute is pattern's input schema with a description per property.
func fromRoute(pattern string, describe map[string]string) map[string]any {
	sc, ok := apidoc.InputSchema(pattern)
	if !ok {
		panic("mcp: no input schema for " + pattern + " in internal/apidoc")
	}
	props, _ := sc["properties"].(map[string]any)
	for name, d := range describe {
		p, _ := props[name].(map[string]any)
		if p == nil {
			panic("mcp: " + pattern + " has no property " + name)
		}
		p["description"] = d
	}
	return sc
}

func (s *Server) toolSet() []tool {
	readOnly := map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false}
	plan := fromRoute(planRoute, map[string]string{
		"action": msg.T("agents.field_action", nil),
		"target": msg.T("agents.field_target", nil),
		"params": msg.T("agents.field_params", nil),
	})
	propose := cloneSchema(plan)
	propose["properties"].(map[string]any)["rationale"] = map[string]any{"type": "string", "maxLength": action.MaxRationale, "description": msg.T("agents.field_rationale", nil)}
	propose["required"] = append(slices.Clone(stringsOf(propose["required"])), "rationale")
	return []tool{
		{Name: "search", Title: msg.T("agents.title_search", nil), Description: msg.T("agents.tool_search", nil), Annotations: readOnly,
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"query": map[string]any{"type": "string", "maxLength": 200, "description": msg.T("agents.field_query", nil)},
				"kind":  map[string]any{"type": "string", "maxLength": 50, "description": msg.T("agents.field_kind", nil)},
				"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "description": msg.T("agents.field_limit", nil)},
			}},
			call: s.search},
		{Name: "get_resource", Title: msg.T("agents.title_get_resource", nil), Description: msg.T("agents.tool_get_resource", nil), Annotations: readOnly,
			InputSchema: fromRoute(resourceRoute, map[string]string{"resource": msg.T("agents.field_resource", nil)}),
			call:        s.getResource},
		{Name: "plan", Title: msg.T("agents.title_plan", nil), Description: msg.T("agents.tool_plan", nil),
			Annotations: map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": false, "openWorldHint": false},
			InputSchema: plan, call: s.plan},
		{Name: "propose", Title: msg.T("agents.title_propose", nil), Description: msg.T("agents.tool_propose", nil),
			Annotations: map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": false},
			InputSchema: propose, call: s.propose},
		{Name: "get_proposal", Title: msg.T("agents.title_get_proposal", nil), Description: msg.T("agents.tool_get_proposal", nil), Annotations: readOnly,
			InputSchema: map[string]any{"type": "object", "required": []string{"proposal"}, "properties": map[string]any{
				"proposal": map[string]any{"type": "string", "maxLength": 100, "description": msg.T("agents.field_proposal", nil)},
			}},
			call: s.getProposal},
	}
}

func stringsOf(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		var out []string
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// cloneSchema copies a schema's maps, so one tool's edits don't reach another's.
func cloneSchema(m map[string]any) map[string]any {
	out := maps.Clone(m)
	for k, v := range out {
		if sub, ok := v.(map[string]any); ok {
			out[k] = cloneSchema(sub)
		}
	}
	return out
}

func (s *Server) toolList() []tool { return s.tools }

// toolResult is a tools/call result: the structured content, and the same
// as JSON text for clients that read only text.
type toolResult struct {
	Content    []content `json:"content"`
	Structured any       `json:"structuredContent,omitempty"`
	IsError    bool      `json:"isError,omitempty"`
}

type content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func result(v any, isErr bool) toolResult {
	b, _ := json.Marshal(v)
	return toolResult{Content: []content{{Type: "text", Text: string(b)}}, Structured: v, IsError: isErr}
}

// failure is a refusal, as a tool's error result: a code, the HTTP status
// it stands for (429 for a limit), and the hub's sentence, which can name
// things from the lab, so it's marked untrusted too.
type failure struct {
	Error struct {
		Code    string     `json:"code"`
		Status  int        `json:"status"`
		Message *Untrusted `json:"message"`
	} `json:"error"`
}

// refused is a tool error a call can return.
type refused struct {
	code   string
	status int
	m      string
}

func (r *refused) Error() string { return r.m }

func refusal(code string, status int, m msg.M) *refused {
	return &refused{code: code, status: status, m: m.Text()}
}

func (s *Server) callTool(ctx context.Context, v viewer, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, paramsErr("tools/call", err)
	}
	i := slices.IndexFunc(s.tools, func(t tool) bool { return t.Name == p.Name })
	if i < 0 {
		return nil, &rpcError{Code: codeInvalidParams, Message: msg.New("agents.mcp_no_tool", "tool", clip(p.Name, 100)).Text()}
	}
	if len(p.Arguments) == 0 || string(p.Arguments) == "null" {
		p.Arguments = json.RawMessage("{}")
	}
	out, err := s.tools[i].call(ctx, v, p.Arguments)
	if err != nil {
		var f failure
		var r *refused
		var ar *action.Refusal
		switch {
		case errors.As(err, &r):
			f.Error.Code, f.Error.Status, f.Error.Message = r.code, r.status, untrusted(r.m)
		case errors.As(err, &ar):
			f.Error.Code, f.Error.Status, f.Error.Message = ar.Code, ar.Status, untrusted(ar.Message)
		default:
			var re *rpcError
			if errors.As(err, &re) {
				return nil, re
			}
			s.log.Error("mcp: a tool failed", "tool", p.Name, "err", err)
			f.Error.Code, f.Error.Status, f.Error.Message = "internal", 500, untrusted(msg.T("agents.mcp_internal", nil))
		}
		return result(f, true), nil
	}
	return result(out, false), nil
}

// args decodes a tool's arguments strictly: unknown fields are refused, so
// a typo isn't silently ignored.
func args(raw json.RawMessage, tool string, dst any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return paramsErr(tool, err)
	}
	return nil
}

// needGraph is the graph view v's graph.read scopes allow, or a refusal.
func (s *Server) needGraph(ctx context.Context, v viewer) (*view, error) {
	reads := v.graphReads()
	if len(reads) == 0 {
		return nil, refusal("forbidden", 403, msg.New("agents.mcp_needs_scope", "scope", authz.ScopeGraphRead))
	}
	return s.graphView(ctx, v, reads), nil
}

func (s *Server) search(ctx context.Context, v viewer, raw json.RawMessage) (any, error) {
	var a struct {
		Query string `json:"query"`
		Kind  string `json:"kind"`
		Limit int    `json:"limit"`
	}
	if err := args(raw, "search", &a); err != nil {
		return nil, err
	}
	gv, err := s.needGraph(ctx, v)
	if err != nil {
		return nil, err
	}
	if a.Limit <= 0 || a.Limit > 50 {
		a.Limit = 20
	}
	words := strings.Fields(strings.ToLower(clip(a.Query, 200)))
	found := []Resource{}
	total := 0
	for _, r := range gv.resources() {
		if a.Kind != "" && string(r.Kind) != a.Kind {
			continue
		}
		if !matchesWords(r, words) {
			continue
		}
		total++
		if len(found) < a.Limit {
			found = append(found, resourceOut(r, false))
		}
	}
	return map[string]any{"resources": found, "total": total, "graph_version": gv.version}, nil
}

func matchesWords(r *graph.Resource, words []string) bool {
	if len(words) == 0 {
		return true
	}
	var b strings.Builder
	b.WriteString(strings.ToLower(string(r.ID) + " " + r.Name + " " + string(r.Kind) + " " + r.State + " " + string(r.Health)))
	for k, v := range r.Labels {
		if !secretKey.MatchString(k) {
			b.WriteString(" " + strings.ToLower(k+" "+v))
		}
	}
	hay := b.String()
	for _, w := range words {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

func (s *Server) getResource(ctx context.Context, v viewer, raw json.RawMessage) (any, error) {
	var a struct {
		Resource string `json:"resource"`
	}
	if err := args(raw, "get_resource", &a); err != nil {
		return nil, err
	}
	gv, err := s.needGraph(ctx, v)
	if err != nil {
		return nil, err
	}
	if !gv.sees(a.Resource) {
		return nil, refusal("not_found", 404, msg.New("agents.mcp_not_visible"))
	}
	r := gv.doc.Resources[graph.RID(a.Resource)]
	out := map[string]any{
		"resource":  resourceOut(r, true),
		"relations": gv.relations(a.Resource),
		"causes":    causesOut(s.topics.Causes.Of(a.Resource)),
	}
	type offer struct {
		Action      string     `json:"action"`
		Tier        string     `json:"tier"`
		Available   bool       `json:"available"`
		Label       *Untrusted `json:"label,omitempty"`
		Unavailable *Untrusted `json:"unavailable,omitempty"`
		Params      any        `json:"params,omitempty"`
	}
	offers := []offer{}
	if s.engine != nil {
		if list, err := s.engine.ActionsFor(ctx, v.actor, a.Resource); err == nil {
			for _, o := range list {
				x := offer{Action: o.ID, Tier: o.Tier.String(), Available: o.Available, Label: untrusted(o.Label), Unavailable: untrusted(o.Unavailable)}
				if len(o.Params) > 0 {
					x.Params = o.Params
				}
				offers = append(offers, x)
			}
		}
	}
	out["actions"] = offers
	return out, nil
}

// planArgs are plan's and propose's arguments.
type planArgs struct {
	Action    string          `json:"action"`
	Target    string          `json:"target"`
	Params    json.RawMessage `json:"params,omitempty"`
	Rationale *string         `json:"rationale,omitempty"`
}

// planFor plans a's action for v: the target must be one v's graph.read
// scopes let it see, so a narrowed token can't plan past its narrowing.
func (s *Server) planFor(ctx context.Context, v viewer, a planArgs, rationale string) (*action.Plan, *view, error) {
	gv, err := s.needGraph(ctx, v)
	if err != nil {
		return nil, nil, err
	}
	if !gv.sees(a.Target) {
		return nil, nil, refusal("not_found", 404, msg.New("agents.mcp_not_visible"))
	}
	p, err := s.engine.Plan(ctx, v.actor, action.PlanRequest{Action: a.Action, Target: a.Target, Params: a.Params, Rationale: rationale})
	if err != nil {
		return nil, nil, err
	}
	return p, gv, nil
}

// visiblePlan is planOut with the blast radius narrowed to what v may see,
// and a count of the rest, so the agent knows it's bigger.
func visiblePlan(p *action.Plan, gv *view) map[string]any {
	out := planOut(*p)
	all := out.Impact
	out.Impact = []action.Impact{}
	hidden := 0
	for _, im := range all {
		if gv.sees(im.Resource) {
			out.Impact = append(out.Impact, im)
		} else {
			hidden++
		}
	}
	return map[string]any{"plan": out, "impact_not_shown": hidden}
}

func (s *Server) plan(ctx context.Context, v viewer, raw json.RawMessage) (any, error) {
	var a planArgs
	if err := args(raw, "plan", &a); err != nil {
		return nil, err
	}
	if a.Rationale != nil {
		return nil, paramsErr("plan", errors.New("rationale belongs to propose"))
	}
	p, gv, err := s.planFor(ctx, v, a, "")
	if err != nil {
		return nil, err
	}
	return visiblePlan(p, gv), nil
}

func (s *Server) propose(ctx context.Context, v viewer, raw json.RawMessage) (any, error) {
	var a planArgs
	if err := args(raw, "propose", &a); err != nil {
		return nil, err
	}
	rationale := ""
	if a.Rationale != nil {
		if n := len([]rune(*a.Rationale)); n > action.MaxRationale {
			return nil, refusal("invalid", 422, msg.New("agents.rationale_long", "max", action.MaxRationale))
		}
		rationale = action.CleanRationale(*a.Rationale)
	}
	if rationale == "" {
		return nil, refusal("invalid", 422, msg.New("agents.rationale_missing"))
	}
	p, gv, err := s.planFor(ctx, v, a, rationale)
	if err != nil {
		return nil, err
	}
	task, apv, err := s.engine.ApproveOrSubmit(ctx, v.actor, p.ID, action.ApproveRequest{Hash: p.Hash}, "")
	if err != nil {
		return nil, err
	}
	if apv == nil || task.ID != "" {
		// Never: an agent's plan always waits for a person.
		s.log.Error("mcp: an agent's plan ran without a person", "plan", p.ID, "task", task.ID)
		return nil, errors.New("mcp: an agent's plan didn't wait for a person")
	}
	out := visiblePlan(p, gv)
	out["proposal"] = proposalOut(*apv)
	out["nth_today"] = apv.NthToday
	return out, nil
}

func (s *Server) getProposal(ctx context.Context, v viewer, raw json.RawMessage) (any, error) {
	var a struct {
		Proposal string `json:"proposal"`
	}
	if err := args(raw, "get_proposal", &a); err != nil {
		return nil, err
	}
	return s.proposal(ctx, v, a.Proposal)
}

// proposal is one of v's own proposals, with its task once it runs.
func (s *Server) proposal(ctx context.Context, v viewer, id string) (any, error) {
	apv, err := s.engine.Approval(ctx, v.actor, clip(id, 100))
	if errors.Is(err, action.ErrNotFound) || (err == nil && (apv.Originator.ID != v.actor.ID || apv.Originator.Kind != action.KindAgent)) {
		// Only its own: Approval would also show others' to an approver.
		return nil, refusal("not_found", 404, msg.New("agents.mcp_no_proposal"))
	} else if err != nil {
		return nil, err
	}
	out := proposalOut(apv)
	if apv.Task != nil {
		if t, err := s.engine.Task(ctx, v.actor, *apv.Task); err == nil {
			out.Task = taskOut(t, v.logs())
		}
	}
	return out, nil
}

// ── resources ────────────────────────────────────────────────────────────

const scheme = "chadash://"

func (s *Server) resourceList() []map[string]any {
	return []map[string]any{
		{"uri": scheme + "graph", "name": "graph", "title": msg.T("agents.title_graph", nil), "description": msg.T("agents.res_graph", nil), "mimeType": "application/json"},
		{"uri": scheme + "incidents", "name": "incidents", "title": msg.T("agents.title_incidents", nil), "description": msg.T("agents.res_incidents", nil), "mimeType": "application/json"},
	}
}

func (s *Server) resourceTemplates() []map[string]any {
	return []map[string]any{
		{"uriTemplate": scheme + "resources/{id}", "name": "resource", "title": msg.T("agents.title_resource", nil), "description": msg.T("agents.res_resource", nil), "mimeType": "application/json"},
		{"uriTemplate": scheme + "causes/{id}", "name": "causes", "title": msg.T("agents.title_causes", nil), "description": msg.T("agents.res_causes", nil), "mimeType": "application/json"},
		{"uriTemplate": scheme + "proposals/{id}", "name": "proposal", "title": msg.T("agents.title_proposal", nil), "description": msg.T("agents.res_proposal", nil), "mimeType": "application/json"},
	}
}

// codeNoResource is MCP's "resource not found".
const codeNoResource = -32002

func (s *Server) readResource(ctx context.Context, v viewer, params json.RawMessage) (any, *rpcError) {
	var p struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.URI == "" {
		return nil, paramsErr("resources/read", err)
	}
	notFound := &rpcError{Code: codeNoResource, Message: msg.New("agents.mcp_no_resource", "uri", clip(p.URI, 200)).Text()}
	rest, ok := strings.CutPrefix(p.URI, scheme)
	if !ok {
		return nil, notFound
	}
	kind, arg, _ := strings.Cut(rest, "/")
	if a, err := url.PathUnescape(arg); err == nil {
		arg = a
	}
	var body any
	var err error
	switch {
	case kind == "graph" && arg == "":
		var gv *view
		if gv, err = s.needGraph(ctx, v); err == nil {
			rs := gv.resources()
			list := make([]Resource, 0, len(rs))
			for _, r := range rs {
				list = append(list, resourceOut(r, true))
			}
			body = map[string]any{"version": gv.version, "resources": list, "relations": gv.relations("")}
		}
	case kind == "incidents" && arg == "":
		if len(v.inboxReads()) == 0 {
			err = refusal("forbidden", 403, msg.New("agents.mcp_needs_scope", "scope", authz.ScopeInboxRead))
		} else {
			body = map[string]any{"incidents": s.incidents(ctx, v)}
		}
	case kind == "resources" && arg != "":
		body, err = s.getResource(ctx, v, mustArgs(map[string]string{"resource": arg}))
	case kind == "causes" && arg != "":
		var gv *view
		if gv, err = s.needGraph(ctx, v); err == nil {
			if !gv.sees(arg) {
				err = refusal("not_found", 404, msg.New("agents.mcp_not_visible"))
			} else {
				body = map[string]any{"resource": arg, "causes": causesOut(s.topics.Causes.Of(arg))}
			}
		}
	case kind == "proposals" && arg != "":
		body, err = s.proposal(ctx, v, arg)
	default:
		return nil, notFound
	}
	if err != nil {
		var r *refused
		if errors.As(err, &r) {
			return nil, &rpcError{Code: codeNoResource, Message: r.m, Data: map[string]any{"code": r.code, "status": r.status}}
		}
		s.log.Error("mcp: reading a resource failed", "uri", clip(p.URI, 200), "err", err)
		return nil, &rpcError{Code: codeInternal, Message: msg.T("agents.mcp_internal", nil)}
	}
	b, _ := json.Marshal(body)
	return map[string]any{"contents": []map[string]any{{"uri": p.URI, "mimeType": "application/json", "text": string(b)}}}, nil
}

func mustArgs(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
