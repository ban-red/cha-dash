package apidoc

import (
	"dash.cha.sh/internal/correlate"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/runtime"
)

// Mirrors of what internal/runtime's and internal/correlate's handlers
// read and write.
type (
	idBody struct {
		ID string `json:"id"`
	}
	invalidFields struct {
		httpx.ErrorBody
		Fields []integration.FieldError `json:"fields"`
	}
	decisionRequest struct {
		From    graph.RID         `json:"from"`
		Type    graph.RelType     `json:"type"`
		To      graph.RID         `json:"to"`
		Verdict correlate.Verdict `json:"verdict"`
	}
)

func init() {
	add("integrations", map[string]Op{
		"GET /api/integrations/catalog":   {Summary: "The integrations this hub has: their settings, capabilities and actions.", Resp: []runtime.CatalogEntry{}},
		"GET /api/integrations/instances": {Summary: "The integration instances and their status.", Resp: []runtime.InstanceView{}},
		"POST /api/integrations/instances": {Summary: "Add an integration instance.", Body: runtime.CreateRequest{}, Status: 201, Resp: idBody{}, Fail: map[int]any{422: invalidFields{}},
			Errors: []string{"invalid", "unknown_integration", "conflict"}},
		"GET /api/integrations/instances/{id}": {Summary: "One integration instance.", Resp: runtime.InstanceView{}},
		"PUT /api/integrations/instances/{id}": {Summary: "Change an integration instance; secrets left out are kept.", Body: runtime.UpdateRequest{}, Resp: runtime.InstanceView{}, Fail: map[int]any{422: invalidFields{}},
			Errors: []string{"invalid", "conflict"}},
		"DELETE /api/integrations/instances/{id}":            {Summary: "Remove an integration instance."},
		"POST /api/integrations/test":                        {Summary: "Test settings before saving them.", Body: runtime.TestRequest{}, Resp: integration.TestResult{}, Fail: map[int]any{422: invalidFields{}}},
		"POST /api/integrations/instances/{id}/test":         {Summary: "Test a saved instance.", Resp: integration.TestResult{}},
		"POST /api/integrations/instances/{id}/refresh":      {Summary: "Poll an instance now.", Status: 202},
		"GET /api/integrations/instances/{id}/diagnostics":   {Summary: "An instance's diagnostics, as a JSON attachment without secrets.", Resp: runtime.Diagnostics{}},
		"GET /api/integrations/instances/{id}/picture":       {Summary: "A camera's latest frame.", Media: "image/*"},
		"POST /api/integrations/authorize/{integration}":     {Summary: "Start signing in to an integration's upstream (a device code or a link).", Resp: runtime.AuthStart{}},
		"GET /api/integrations/authorize/{integration}/{id}": {Summary: "Poll a sign-in; when done, it carries the new credential once.", Resp: runtime.AuthStatus{}},
	})
	add("correlate", map[string]Op{
		"GET /api/correlate/links":                     {Summary: "Things several sources report as one, and why.", Resp: []correlate.Link{}},
		"GET /api/correlate/suggestions":               {Summary: "Weak matches waiting for a decision.", Resp: []correlate.Suggestion{}},
		"GET /api/correlate/decisions":                 {Summary: "The decisions made about matches.", Resp: []correlate.Decision{}},
		"POST /api/correlate/suggestions/{id}/confirm": {Summary: "Confirm a suggested match.", Resp: correlate.Decision{}},
		"POST /api/correlate/suggestions/{id}/reject":  {Summary: "Reject a suggested match.", Resp: correlate.Decision{}},
		"POST /api/correlate/decisions":                {Summary: "Decide about a pair directly.", Body: decisionRequest{}, Status: 201, Resp: correlate.Decision{}},
		"DELETE /api/correlate/decisions/{id}":         {Summary: "Forget a decision."},
	})
}
