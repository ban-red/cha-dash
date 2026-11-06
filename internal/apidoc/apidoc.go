// Package apidoc builds the hub's OpenAPI 3.1 document from the route
// table (internal/authz/routes.go) and a row per route here, in the area
// files (auth.go, boards.go, stacks.go, …). Schemas are generated from the
// Go types the handlers encode and decode (schema.go); where a handler
// writes an anonymous struct or a map, a mirror type in the area file
// stands in for it. TestEveryRouteHasASchema fails on a route without a
// row, and on a row without a route.
//
// Who may call a route comes from its authz row: x-chadash-access (the
// least role, or public, node_key, signature) and x-chadash-scope (what an
// API token needs; empty means session only).
package apidoc

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/buildinfo"
)

// Op describes one route.
type Op struct {
	// Summary is a short plain sentence.
	Summary string
	// Query are the query parameters, all optional unless Required.
	Query []Param
	// Body is a value of the JSON request body's type; nil when there's none.
	Body any
	// BodyMedia is the request body's media type when it isn't JSON (Body
	// is then ignored).
	BodyMedia string
	// Status is the success status: 200 by default, 204 when there's no
	// Resp and no Media.
	Status int
	// Resp is a value of the JSON success body's type (OneOf for several).
	Resp any
	// Media is the success body's media type when it isn't JSON: a file,
	// a stream, an image or the web app.
	Media string
	// Upgrade marks a WebSocket: the success answer is 101.
	Upgrade bool
	// Redirect marks a browser redirect: the success answer is 303.
	Redirect bool
	// Errors are error codes worth knowing beyond the common ones.
	Errors []string
	// Fail are error answers whose body carries more than the error, by
	// status.
	Fail map[int]any
	// Deprecated is the version that deprecated the route ("0.5.0"): the
	// document marks it, and it stays for at least one minor version more
	// before it goes (CONTRIBUTING.md, "Deprecations"; TestDeprecationRule).
	Deprecated string
}

// Param is a query parameter.
type Param struct {
	Name, Desc string
	Required   bool
	// Many: it may be repeated.
	Many bool
}

// OneOf is a response that takes one of several shapes.
type OneOf []any

var (
	rows = map[string]Op{}
	tags = map[string]string{}
)

// add registers an area's rows under tag. A pattern listed twice panics.
func add(tag string, ops map[string]Op) {
	for p, op := range ops {
		if _, dup := rows[p]; dup {
			panic("apidoc: row listed twice: " + p)
		}
		rows[p], tags[p] = op, tag
	}
}

// Rows are the documented patterns, sorted.
func Rows() []string { return slices.Sorted(maps.Keys(rows)) }

// JSON is the document, built once.
var JSON = sync.OnceValue(func() []byte {
	b, err := json.MarshalIndent(Document(), "", "  ")
	if err != nil {
		panic("apidoc: " + err.Error())
	}
	return b
})

var pathParam = regexp.MustCompile(`\{([^}]+)\}`)

// Document builds the OpenAPI document. Routes in authz.Routes without a
// row are left out (the test catches them).
func Document() map[string]any {
	s := newSchemas()
	paths := map[string]any{}
	for _, route := range authz.Routes {
		op, ok := rows[route.Pattern]
		if !ok {
			continue
		}
		method, path, hasMethod := strings.Cut(route.Pattern, " ")
		if !hasMethod {
			method, path = "GET", route.Pattern
		}
		item, _ := paths[path].(map[string]any)
		if item == nil {
			item = map[string]any{}
			paths[path] = item
		}
		o := operation(s, route, op, method, path)
		if !hasMethod {
			o["x-chadash-any-method"] = true
		}
		item[strings.ToLower(method)] = o
	}
	errBody := s.of(reflect.TypeFor[ErrorBody]())
	s.components["ErrorBody"] = errBody
	return map[string]any{
		"openapi":           "3.1.0",
		"jsonSchemaDialect": "https://spec.openapis.org/oas/3.1/dialect/base",
		"info": map[string]any{
			"title":   "Cha Dash",
			"version": buildinfo.Version,
			"summary": "The hub's HTTP API.",
			"description": "Every route the hub serves, generated from its route table. " +
				"x-chadash-access is the least a caller needs (a role, or public, node_key, signature, agent_token); " +
				"x-chadash-scope is what an API token needs, and empty means session only. " +
				"Errors answer {\"error\":{\"code\",\"message\"}}.",
			"license": map[string]any{"name": "AGPL-3.0-or-later", "identifier": "AGPL-3.0-or-later"},
		},
		"paths": paths,
		"components": map[string]any{
			"schemas": s.components,
			"securitySchemes": map[string]any{
				"session": map[string]any{"type": "apiKey", "in": "cookie", "name": "chadash_session",
					"description": "A signed-in session's cookie. Unsafe requests also need a same-origin request."},
				"token": map[string]any{"type": "http", "scheme": "bearer",
					"description": "An API token (cdt_…), on /api/ routes whose x-chadash-scope isn't empty."},
				"display": map[string]any{"type": "apiKey", "in": "cookie", "name": "chadash_display",
					"description": "A display's cookie, set by POST /api/display/claim and sent only under /api/display. A script may send the display's secret (cdd_…) as a bearer token instead."},
			},
			"responses": map[string]any{
				"Error": map[string]any{"description": "An error.",
					"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/ErrorBody"}}}},
			},
		},
	}
}

// ErrorBody is every API error's shape (httpx.ErrorBody), with the message
// ID and its arguments that internal/msg adds.
type ErrorBody struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		ID      string         `json:"id,omitempty"`
		Args    map[string]any `json:"args,omitempty"`
	} `json:"error"`
}

func operation(s *schemas, route authz.Route, op Op, method, path string) map[string]any {
	o := map[string]any{
		"operationId": operationID(method, path),
		"summary":     op.Summary,
		"tags":        []string{tags[route.Pattern]},
	}
	var params []any
	for _, m := range pathParam.FindAllStringSubmatch(path, -1) {
		params = append(params, map[string]any{"name": m[1], "in": "path", "required": true, "schema": map[string]any{"type": "string"}})
	}
	for _, q := range op.Query {
		p := map[string]any{"name": q.Name, "in": "query", "description": q.Desc, "schema": map[string]any{"type": "string"}}
		if q.Many {
			p["schema"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
			p["explode"] = true
		}
		if q.Required {
			p["required"] = true
		}
		params = append(params, p)
	}
	if params != nil {
		o["parameters"] = params
	}
	switch {
	case op.BodyMedia != "":
		o["requestBody"] = map[string]any{"required": true, "content": map[string]any{op.BodyMedia: map[string]any{}}}
	case op.Body != nil:
		o["requestBody"] = map[string]any{"required": true,
			"content": map[string]any{"application/json": map[string]any{"schema": s.of(reflect.TypeOf(op.Body))}}}
	}

	responses := map[string]any{"default": map[string]any{"$ref": "#/components/responses/Error"}}
	status := op.Status
	switch {
	case op.Upgrade:
		responses["101"] = map[string]any{"description": "Switching to a WebSocket."}
	case op.Redirect:
		responses["303"] = map[string]any{"description": "A redirect, for a browser."}
	case op.Media != "":
		if status == 0 {
			status = http.StatusOK
		}
		responses[strconv.Itoa(status)] = map[string]any{"description": http.StatusText(status),
			"content": map[string]any{op.Media: map[string]any{}}}
	case op.Resp != nil:
		if status == 0 {
			status = http.StatusOK
		}
		var sc map[string]any
		if alts, ok := op.Resp.(OneOf); ok {
			var list []any
			for _, a := range alts {
				list = append(list, s.of(reflect.TypeOf(a)))
			}
			sc = map[string]any{"oneOf": list}
		} else {
			sc = s.of(reflect.TypeOf(op.Resp))
		}
		responses[strconv.Itoa(status)] = map[string]any{"description": http.StatusText(status),
			"content": map[string]any{"application/json": map[string]any{"schema": sc}}}
	default:
		if status == 0 {
			status = http.StatusNoContent
		}
		responses[strconv.Itoa(status)] = map[string]any{"description": http.StatusText(status)}
	}
	for code, body := range op.Fail {
		responses[strconv.Itoa(code)] = map[string]any{"description": http.StatusText(code),
			"content": map[string]any{"application/json": map[string]any{"schema": s.of(reflect.TypeOf(body))}}}
	}
	o["responses"] = responses
	if len(op.Errors) > 0 {
		o["x-chadash-errors"] = op.Errors
	}

	if op.Deprecated != "" {
		o["deprecated"] = true
		o["x-chadash-deprecated-in"] = op.Deprecated
	}
	o["x-chadash-access"] = access(route.Access)
	switch route.Access {
	case authz.Public, authz.NodeKey, authz.Signature:
		o["security"] = []any{}
	case authz.AgentToken:
		// An agent token, never a session or a plain API token.
		o["x-chadash-scope"] = "agent"
		o["security"] = []any{map[string]any{"token": []string{}}}
	case authz.DisplayToken:
		// A display's cookie (or Bearer cdd_…), never a session or an API token.
		o["x-chadash-scope"] = "display"
		o["security"] = []any{map[string]any{"display": []string{}}}
	default:
		o["x-chadash-scope"] = route.Token
		sec := []any{map[string]any{"session": []string{}}}
		if route.Token != "" {
			sec = append(sec, map[string]any{"token": []string{}})
		}
		o["security"] = sec
	}
	if route.Optional {
		o["x-chadash-optional"] = true
	}
	return o
}

// access names an authz.Access for the document.
func access(a authz.Access) string {
	switch a {
	case authz.Public:
		return "public"
	case authz.SignedIn:
		return "signed_in"
	case authz.Viewer:
		return "viewer"
	case authz.Operator:
		return "operator"
	case authz.Admin:
		return "admin"
	case authz.Owner:
		return "owner"
	case authz.NodeKey:
		return "node_key"
	case authz.Signature:
		return "signature"
	case authz.AgentToken:
		return "agent_token"
	case authz.DisplayToken:
		return "display"
	}
	panic(fmt.Sprintf("apidoc: unknown access %d", a))
}

// operationID is a stable ID from the method and path:
// "GET /api/boards/{id}/layout" is "get_api_boards_id_layout".
func operationID(method, path string) string {
	id := strings.ToLower(method) + strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, strings.NewReplacer("{", "", "}", "").Replace(path))
	for strings.Contains(id, "__") {
		id = strings.ReplaceAll(id, "__", "_")
	}
	return strings.TrimSuffix(id, "_")
}
