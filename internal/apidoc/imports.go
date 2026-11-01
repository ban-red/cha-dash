package apidoc

import (
	"dash.cha.sh/internal/export"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/importer"
	"dash.cha.sh/internal/importer/importhttp"
	"dash.cha.sh/internal/onboarding"
)

// Mirrors of what the import, export and onboarding handlers read and
// write.
type (
	detectFile struct {
		Name   string `json:"name"`
		Head   string `json:"head"`
		Base64 bool   `json:"base64"`
	}
	detectRequest struct {
		Files []detectFile `json:"files"`
	}
	// importRequest is a source's files by role (a role's key at the top
	// level works too), the choices made in the preview and the plan's hash.
	importRequest struct {
		Files    map[string]string `json:"files"`
		Names    map[string]string `json:"names,omitempty"`
		Choices  importer.Choices  `json:"choices,omitzero"`
		PlanHash string            `json:"plan_hash,omitempty"`
	}
	planChanged struct {
		Error  httpx.ErrorDetail `json:"error"`
		Result *importer.Result  `json:"result"`
	}
	recordBody struct {
		Record *importer.Record `json:"record"`
	}
	importList struct {
		Imports []importer.HistoryEntry `json:"imports"`
	}
	passphraseBody struct {
		Passphrase string `json:"passphrase"`
	}
	homeFileRequest struct {
		YAML       string `json:"yaml"`
		Passphrase string `json:"passphrase,omitempty"`
	}
	homeFilePlan struct {
		Plan *export.Plan `json:"plan"`
	}
	keysBody struct {
		Keys []string `json:"keys"`
	}
	dismissedKeys struct {
		Dismissed []string `json:"dismissed"`
	}
)

var dryRun = []Param{{Name: "dry_run", Desc: "1 previews the plan and changes nothing."}}

func init() {
	src := func(name string) map[string]Op {
		return map[string]Op{
			"POST /api/import/" + name: {Summary: "Import from " + name + ": preview with dry_run=1, then apply with the plan's hash.", Query: dryRun,
				Body: importRequest{}, Resp: OneOf{&importer.Result{}, importer.Applied{}}, Fail: map[int]any{409: planChanged{}},
				Errors: []string{"invalid", "invalid_config", "invalid_choices", "plan_changed"}},
			"GET /api/import/" + name: {Summary: "The last import from " + name + ".", Resp: recordBody{}},
		}
	}
	for _, name := range []string{"homepage", "homarr", "yaml", "discovery"} {
		add("import", src(name))
	}
	add("import", map[string]Op{
		"POST /api/import/detect":       {Summary: "Say which source some files come from, from their names and first bytes.", Body: detectRequest{}, Resp: importhttp.Detected{}},
		"GET /api/imports":              {Summary: "The import history, newest first.", Resp: importList{}},
		"GET /api/imports/{id}":         {Summary: "One import and what it made.", Resp: importer.HistoryEntry{}},
		"POST /api/imports/{id}/revert": {Summary: "Revert an import: what it made and nothing changed since. dry_run=1 previews.", Query: []Param{{Name: "dry_run", Desc: "1 previews."}}, Resp: importer.RevertPlan{}, Errors: []string{"reverted", "step_up_required"}},
		"GET /api/export":               {Summary: "The hub as a Home file, secrets as placeholders.", Media: "application/yaml"},
		"POST /api/export":              {Summary: "The hub as a Home file with its secrets in a bundle encrypted with the passphrase.", Body: passphraseBody{}, Media: "application/yaml", Errors: []string{"weak_passphrase"}},
		"POST /api/import/chadash":      {Summary: "Import a Home file: dry_run=1 plans, otherwise it applies.", Query: dryRun, Body: homeFileRequest{}, Resp: OneOf{homeFilePlan{}, &export.Applied{}}},
	})
	add("onboarding", map[string]Op{
		"GET /api/onboarding":           {Summary: "The first-run wizard's state.", Resp: onboarding.State{}},
		"PUT /api/onboarding":           {Summary: "Save the first-run wizard's state.", Body: onboarding.Update{}, Resp: onboarding.State{}},
		"POST /api/onboarding/discover": {Summary: "Look for services on the network (the wizard's step).", Body: onboarding.DiscoverRequest{}, Resp: onboarding.DiscoverResult{}},
		"POST /api/discover":            {Summary: "Look for services on the network.", Body: onboarding.DiscoverRequest{}, Resp: onboarding.DiscoverResult{}},
		"GET /api/discover/dismissed":   {Summary: "The discovery results dismissed.", Resp: dismissedKeys{}},
		"POST /api/discover/dismiss":    {Summary: "Dismiss discovery results.", Body: keysBody{}, Resp: dismissedKeys{}},
		"POST /api/discover/restore":    {Summary: "Restore dismissed discovery results.", Body: keysBody{}, Resp: dismissedKeys{}},
	})
}
