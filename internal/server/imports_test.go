package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

// Revert asks for a step-up before it removes connections, and History
// lists the import.
func TestRevertNeedsAStepUpForConnections(t *testing.T) {
	e, _ := auditEnv(t)
	owner := signIn(t, e)
	board := "apiVersion: chadash/v1alpha1\nkind: Board\nname: Links\nsections:\n  - tiles:\n      - {name: Wiki, href: https://wiki.example}\n"
	status, _, b := e.call(t, owner, "POST", "/api/import/yaml", map[string]any{"files": map[string]string{"board_yaml": board}})
	if status != http.StatusOK {
		t.Fatalf("import: %d %s", status, b)
	}
	var applied struct {
		ImportID string `json:"import_id"`
	}
	_ = json.Unmarshal(b, &applied)

	status, _, b = e.call(t, owner, "GET", "/api/imports", nil)
	var list struct {
		Imports []struct{ ID, Source string } `json:"imports"`
	}
	if _ = json.Unmarshal(b, &list); status != http.StatusOK || len(list.Imports) != 1 || list.Imports[0].Source != "yaml" {
		t.Fatalf("history: %d %s", status, b)
	}

	status, _, b = e.call(t, owner, "POST", "/api/imports/"+applied.ImportID+"/revert?dry_run=1", nil)
	var plan struct{ Connections bool }
	if _ = json.Unmarshal(b, &plan); status != http.StatusOK || !plan.Connections {
		t.Fatalf("dry run: %d %s", status, b)
	}
	if status, code, _ := e.call(t, owner, "POST", "/api/imports/"+applied.ImportID+"/revert", nil); status != http.StatusForbidden || code != "step_up_required" {
		t.Fatalf("revert without a step-up: %d %s", status, code)
	}
	stepUp(t, e, owner)
	if status, _, b := e.call(t, owner, "POST", "/api/imports/"+applied.ImportID+"/revert", nil); status != http.StatusOK {
		t.Fatalf("revert: %d %s", status, b)
	}
	if status, code, _ := e.call(t, owner, "POST", "/api/imports/"+applied.ImportID+"/revert", nil); status != http.StatusConflict || code != "reverted" {
		t.Fatalf("second revert: %d %s", status, code)
	}
}

// A dropped file is named by its first bytes.
func TestDetectNamesTheSource(t *testing.T) {
	e, _ := auditEnv(t)
	owner := signIn(t, e)
	for _, tc := range []struct{ name, head, source string }{
		{"services.yaml", "- Media:\n    - Plex:\n        href: https://plex.example\n", "homepage"},
		{"board.yaml", "apiVersion: chadash/v1alpha1\nkind: Board\nname: X\n", "yaml"},
		{"home.yaml", "apiVersion: chadash/v1alpha1\nkind: Home\n", "chadash"},
		{"default.json", `{"schemaVersion": 2, "configProperties": {}}`, "homarr_legacy"},
		{"notes.txt", "hello", ""},
	} {
		status, _, b := e.call(t, owner, "POST", "/api/import/detect", map[string]any{"files": []map[string]string{{"name": tc.name, "head": tc.head}}})
		var d struct{ Source string }
		if _ = json.Unmarshal(b, &d); status != http.StatusOK || d.Source != tc.source {
			t.Errorf("%s: %d %s", tc.name, status, b)
		}
	}
}
