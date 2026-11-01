package apidoc

import (
	"reflect"
	"slices"
	"strings"
)

// Mirrors of the MCP endpoint's JSON-RPC 2.0 messages (internal/mcp). One
// message per request; a notification or a response is answered 202 with
// no body.
type (
	mcpRequest struct {
		JSONRPC string `json:"jsonrpc"`
		// ID is a string or a number; absent for a notification.
		ID     any    `json:"id,omitempty"`
		Method string `json:"method"`
		Params any    `json:"params,omitempty"`
	}
	mcpError struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    any    `json:"data,omitempty"`
	}
	mcpResponse struct {
		JSONRPC string    `json:"jsonrpc"`
		ID      any       `json:"id"`
		Result  any       `json:"result,omitempty"`
		Error   *mcpError `json:"error,omitempty"`
	}
)

func init() {
	add("agents", map[string]Op{
		"POST /mcp": {Summary: "The MCP endpoint (streamable HTTP, JSON-RPC 2.0) for agent tokens: read the Home Graph and incidents, plan, and propose actions for a person to approve.",
			Body: mcpRequest{}, Resp: mcpResponse{}, Errors: []string{"agents_off", "agent_token_only", "forbidden", "invalid"}},
		"/mcp": {Summary: "Any other method on the MCP endpoint answers 405: there's no event stream and no session to end.", Status: 405, Media: "application/json"},
	})
}

// InputSchema is the JSON Schema of what the route pattern takes, inlined
// (no $ref), for an MCP tool generated from it (internal/mcp), so the tool
// and the route can't drift: its JSON body's type, or its query parameters
// as an object of strings. False for a route without a row, or one that
// takes neither.
func InputSchema(pattern string) (map[string]any, bool) {
	op, ok := rows[pattern]
	if !ok {
		return nil, false
	}
	s := newSchemas()
	switch {
	case op.Body != nil && op.BodyMedia == "":
		out, _ := s.inline(s.of(reflect.TypeOf(op.Body)), 0).(map[string]any)
		return out, out != nil
	case len(op.Query) > 0:
		props := map[string]any{}
		required := []string{}
		for _, q := range op.Query {
			props[q.Name] = map[string]any{"type": "string", "description": q.Desc}
			if q.Required {
				required = append(required, q.Name)
			}
		}
		slices.Sort(required)
		return map[string]any{"type": "object", "properties": props, "required": required}, true
	}
	return nil, false
}

// inline replaces every $ref in v with the component it names, a copy, at
// most a few levels deep (a recursive type stops as an open schema).
func (s *schemas) inline(v any, depth int) any {
	switch v := v.(type) {
	case map[string]any:
		if ref, ok := v["$ref"].(string); ok {
			name := strings.TrimPrefix(ref, "#/components/schemas/")
			c, ok := s.components[name]
			if !ok || c == nil || depth > 6 {
				return map[string]any{}
			}
			return s.inline(c, depth+1)
		}
		out := make(map[string]any, len(v))
		for k, x := range v {
			out[k] = s.inline(x, depth)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = s.inline(x, depth)
		}
		return out
	case []string:
		return slices.Clone(v)
	}
	return v
}
