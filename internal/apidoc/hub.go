package apidoc

import (
	"encoding/json"

	"dash.cha.sh/internal/boards"
	"dash.cha.sh/internal/demo"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/icons"
)

// Mirrors of what internal/boards' handlers write.
type (
	boardList struct {
		Boards []boards.Board `json:"boards"`
	}
	// The layout and tiles are the web app's documents (packages/layout),
	// stored as they come.
	layoutBody struct {
		Revision      *int64          `json:"revision"`
		FormatVersion *int64          `json:"format_version"`
		Layout        json.RawMessage `json:"layout"`
		Tiles         json.RawMessage `json:"tiles,omitempty"`
	}
	revisionBody struct {
		Revision int64 `json:"revision"`
	}
	layoutConflict struct {
		httpx.ErrorBody
		Revision int64 `json:"revision"`
	}
	resourcesBody struct {
		Resources []string `json:"resources"`
	}
	dismissedBody struct {
		Dismissed []string `json:"dismissed"`
	}
)

func init() {
	add("hub", map[string]Op{
		"GET /healthz":                        {Summary: "Liveness: ok when both database pools answer.", Media: "text/plain"},
		"GET /metrics":                        {Summary: "Prometheus metrics, only on the metrics listener (CHADASH_METRICS_LISTEN).", Media: "text/plain; version=0.0.4"},
		"/api/":                               {Summary: "Any other /api/ path: 404 not_found.", Status: 404, Resp: httpx.ErrorBody{}},
		"/":                                   {Summary: "The web app: any path outside /api/ gets index.html.", Media: "text/html"},
		"GET /ws":                             {Summary: "The realtime WebSocket: subscribe to topics.", Upgrade: true},
		"GET /node":                           {Summary: "chadash-node's channel: a WebSocket with mutual TLS inside, authenticated by the node's pinned key.", Upgrade: true},
		"GET /api/demo":                       {Summary: "The demo clock and seed; demo mode only.", Resp: demo.Info{}},
		"GET /api/config/schema.json":         {Summary: "The config schema: every setting the hub and chadash-node take, from flags and the environment, with its version.", Media: "application/schema+json"},
		"GET /api/specs/schema/v1beta1.json":  {Summary: "The spec format's JSON Schema, chadash/v1beta1, the same for every hub of a version.", Media: "application/schema+json"},
		"GET /api/specs/schema/v1alpha1.json": {Summary: "The JSON Schema of chadash/v1alpha1, the spec format before v1beta1, which hubs load through v0.5.x.", Media: "application/schema+json"},
		"GET /api/openapi.json":               {Summary: "This document.", Media: "application/json"},
		"GET /api/icons":                      {Summary: "Which cached icon each tile, origin and instance uses.", Resp: icons.Map{}},
		"GET /api/icons/{id}":                 {Summary: "One cached icon.", Media: "image/*", Query: []Param{{Name: "v", Desc: "The icon's version, for long caching."}}},
	})
	add("boards", map[string]Op{
		"GET /api/boards":                         {Summary: "The boards; the first call creates Home.", Resp: boardList{}},
		"GET /api/boards/{id}/layout":             {Summary: "A board's latest layout, tiles and revision.", Resp: layoutBody{}},
		"PUT /api/boards/{id}/layout":             {Summary: "Save a board's layout at the next revision.", Body: layoutBody{}, Resp: revisionBody{}, Fail: map[int]any{409: layoutConflict{}}},
		"GET /api/boards/{id}/discovery":          {Summary: "The resources dismissed from a board's label discovery.", Resp: dismissedBody{}},
		"POST /api/boards/{id}/discovery/dismiss": {Summary: "Dismiss resources from a board's label discovery.", Body: resourcesBody{}, Resp: dismissedBody{}},
		"POST /api/boards/{id}/discovery/restore": {Summary: "Restore dismissed resources to a board's label discovery.", Body: resourcesBody{}, Resp: dismissedBody{}},
	})
}
