// Package importhttp is the import API every source shares: detecting what
// a dropped file is, the dry run with the Review sheet's choices, applying
// exactly the reviewed plan, History and Revert. Each source (Homepage,
// Homarr, plain YAML) is a Source: its file roles, how to recognise its
// files, and its pure parser.
//
//	POST /api/import/detect                 {files:[{name, head}]} → {source, name, files, unused, missing, message?}
//	POST /api/import/{source}?dry_run=1     {files, names?, choices?} → importer.Result with plan_hash
//	POST /api/import/{source}               the same plus plan_hash → importer.Applied; 409 plan_changed with the fresh result
//	GET  /api/import/{source}               the source's last import record; 404 before the first
//	GET  /api/imports                       {imports:[…]}, newest first
//	GET  /api/imports/{id}                  one History row
//	POST /api/imports/{id}/revert?dry_run=1 → importer.RevertPlan; without dry_run it runs, with a step-up when it removes connections
//
// File contents never reach a log, the audit log or a response; parse
// errors name a file and a line.
package importhttp

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/importer"
)

// Role is one file a source reads.
type Role struct {
	// Key is the role's name in a request's files map (services_yaml).
	Key string `json:"key"`
	// File is the file's usual name, for the sheet (services.yaml).
	File     string `json:"file"`
	Required bool   `json:"required"`
	// Binary roles are sent base64-encoded (a zip).
	Binary bool `json:"binary"`
}

// Source is one importer.
type Source struct {
	ID   string
	Name string
	// MaxBytes bounds a request to this source: the files together, as
	// sent (base64 included).
	MaxBytes int64
	Roles    []Role
	// Detect says which role a file is, from its name and its first bytes
	// (at most HeadBytes). ok is false when it isn't one of this source's.
	Detect func(name string, head []byte) (role string, ok bool)
	// Build parses the files, by role, into a plan. It's pure.
	Build func(files map[string][]byte) (*importer.Result, importer.Secrets, error)
}

// HeadBytes is how much of each file detection reads.
const HeadBytes = 64 << 10

// Register mounts the shared routes and one set per source, each wrapped
// by wrap (the hub's session check plus a reload after a POST).
func Register(mux *http.ServeMux, wrap func(http.Handler) http.Handler, svc *importer.Service, log *slog.Logger, rec *authz.Recorder, sources ...Source) {
	h := &handler{svc: svc, log: log, audit: rec, sources: sources}
	mux.Handle("POST /api/import/detect", wrap(http.HandlerFunc(h.detect)))
	// Patterns are literals, so internal/authz's route test can read them.
	for _, s := range sources {
		post := wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.post(w, r, s) }))
		rec := wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.record(w, r, s) }))
		switch s.ID {
		case "homepage":
			mux.Handle("POST /api/import/homepage", post)
			mux.Handle("GET /api/import/homepage", rec)
		case "homarr":
			mux.Handle("POST /api/import/homarr", post)
			mux.Handle("GET /api/import/homarr", rec)
		case "yaml":
			mux.Handle("POST /api/import/yaml", post)
			mux.Handle("GET /api/import/yaml", rec)
		case "discovery":
			mux.Handle("POST /api/import/discovery", post)
			mux.Handle("GET /api/import/discovery", rec)
		default:
			panic("importhttp: no route for source " + s.ID)
		}
	}
	mux.Handle("GET /api/imports", wrap(http.HandlerFunc(h.history)))
	mux.Handle("GET /api/imports/{id}", wrap(http.HandlerFunc(h.entry)))
	mux.Handle("POST /api/imports/{id}/revert", wrap(http.HandlerFunc(h.revert)))
}

type handler struct {
	svc     *importer.Service
	log     *slog.Logger
	audit   *authz.Recorder
	sources []Source
}

// ── Detect ──────────────────────────────────────────────────────────────────

type detectFile struct {
	Name string `json:"name"`
	// Head is the file's first bytes: text as is, or base64 when Base64.
	Head   string `json:"head"`
	Base64 bool   `json:"base64"`
}

// Detected is POST /api/import/detect's answer.
type Detected struct {
	// Source is a source ID, "chadash" (a Home file: Backup restores it),
	// "homarr_legacy" (Homarr 0.x: not importable) or "" (nothing known).
	Source string `json:"source"`
	Name   string `json:"name,omitempty"`
	// Files maps each recognised file name to its role.
	Files map[string]string `json:"files"`
	// Unused are the files no role took.
	Unused []string `json:"unused"`
	// Missing are required roles no file filled.
	Missing []string `json:"missing"`
	Roles   []Role   `json:"roles,omitempty"`
	Message string   `json:"message,omitempty"`
}

func (h *handler) detect(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Files []detectFile `json:"files"`
	}
	if !httpx.DecodeJSONLimit(w, r, &in, 1<<20) {
		return
	}
	if len(in.Files) == 0 || len(in.Files) > 32 {
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid", "send 1 to 32 files")
		return
	}
	heads := make([][]byte, len(in.Files))
	for i, f := range in.Files {
		b := []byte(f.Head)
		if f.Base64 {
			var err error
			if b, err = base64.StdEncoding.DecodeString(f.Head); err != nil {
				httpx.Error(w, http.StatusUnprocessableEntity, "invalid", "a file's head isn't base64")
				return
			}
		}
		heads[i] = b[:min(len(b), HeadBytes)]
	}
	httpx.WriteJSON(w, http.StatusOK, h.detectFiles(in.Files, heads))
}

func (h *handler) detectFiles(files []detectFile, heads [][]byte) Detected {
	for i, f := range files {
		head := string(heads[i])
		switch {
		case isHomeFile(head):
			return Detected{Source: "chadash", Name: "Cha Dash", Files: map[string]string{f.Name: "yaml"}, Unused: []string{}, Missing: []string{},
				Message: "This is a Cha Dash export. Restore it in Backup."}
		case isHomarrLegacy(f.Name, head):
			return Detected{Source: "homarr_legacy", Name: "Homarr 0.x", Files: map[string]string{}, Unused: []string{f.Name}, Missing: []string{},
				Message: "This is a Homarr 0.x config. Upgrade to Homarr 2 (it imports 0.x configs), make a backup in Management → Tools → Backup, and import that zip here."}
		}
	}
	best := Detected{Files: map[string]string{}, Unused: []string{}, Missing: []string{}}
	for _, s := range h.sources {
		d := Detected{Source: s.ID, Name: s.Name, Files: map[string]string{}, Unused: []string{}, Missing: []string{}, Roles: s.Roles}
		taken := map[string]bool{}
		for i, f := range files {
			if role, ok := s.Detect(f.Name, heads[i]); ok && !taken[role] {
				d.Files[f.Name], taken[role] = role, true
			} else {
				d.Unused = append(d.Unused, f.Name)
			}
		}
		if len(d.Files) <= len(best.Files) {
			continue
		}
		for _, role := range s.Roles {
			if role.Required && !taken[role.Key] {
				d.Missing = append(d.Missing, role.File)
			}
		}
		best = d
	}
	if best.Source == "" {
		for _, f := range files {
			best.Unused = append(best.Unused, f.Name)
		}
		best.Message = "These don't look like a config Cha Dash can import. Pick the source by hand if they are."
	}
	return best
}

func isHomeFile(head string) bool {
	return strings.Contains(head, "apiVersion: chadash/") && strings.Contains(head, "kind: Home")
}

func isHomarrLegacy(name, head string) bool {
	return strings.HasSuffix(strings.ToLower(name), ".json") && strings.Contains(head, `"schemaVersion"`) && strings.Contains(head, `"configProperties"`)
}

// ── Dry run and apply ───────────────────────────────────────────────────────

type request struct {
	Files    map[string]string `json:"files"`
	Names    map[string]string `json:"names"`
	Choices  importer.Choices  `json:"choices"`
	PlanHash string            `json:"plan_hash"`
}

// decode reads a request. Besides files, a role's key at the top level is
// that file (Homepage's original body: {services_yaml, …}).
func decode(w http.ResponseWriter, r *http.Request, s Source) (request, map[string][]byte, bool) {
	var raw map[string]json.RawMessage
	if !httpx.DecodeJSONLimit(w, r, &raw, s.MaxBytes) {
		return request{}, nil, false
	}
	var req request
	for k, v := range raw {
		var err error
		switch k {
		case "files":
			err = json.Unmarshal(v, &req.Files)
		case "names":
			err = json.Unmarshal(v, &req.Names)
		case "choices":
			err = json.Unmarshal(v, &req.Choices)
		case "plan_hash":
			err = json.Unmarshal(v, &req.PlanHash)
		default:
			if !slices.ContainsFunc(s.Roles, func(role Role) bool { return role.Key == k }) {
				httpx.Error(w, http.StatusBadRequest, "bad_json", "unknown field "+strconvQuote(k))
				return req, nil, false
			}
			{
				var text string
				if err = json.Unmarshal(v, &text); err == nil {
					if req.Files == nil {
						req.Files = map[string]string{}
					}
					if _, set := req.Files[k]; !set {
						req.Files[k] = text
					}
				}
			}
		}
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid", k+" has the wrong shape")
			return req, nil, false
		}
	}
	files := map[string][]byte{}
	for _, role := range s.Roles {
		v := req.Files[role.Key]
		if strings.TrimSpace(v) == "" {
			if role.Required {
				httpx.Error(w, http.StatusUnprocessableEntity, "invalid", role.Key+" is required")
				return req, nil, false
			}
			continue
		}
		if role.Binary {
			b, err := base64.StdEncoding.DecodeString(v)
			if err != nil {
				httpx.Error(w, http.StatusUnprocessableEntity, "invalid", role.Key+" isn't base64")
				return req, nil, false
			}
			files[role.Key] = b
		} else {
			files[role.Key] = []byte(v)
		}
	}
	return req, files, true
}

// strconvQuote quotes a field name for an error, cut short: it's a key the
// client sent, never a value.
func strconvQuote(k string) string {
	if len(k) > 40 {
		k = k[:40]
	}
	return strconv.Quote(k)
}

func (h *handler) post(w http.ResponseWriter, r *http.Request, s Source) {
	req, files, ok := decode(w, r, s)
	if !ok {
		return
	}
	dry := false
	switch r.URL.Query().Get("dry_run") {
	case "1", "true":
		dry = true
	case "", "0", "false":
	default:
		httpx.Error(w, http.StatusBadRequest, "invalid", "dry_run must be 1 or 0")
		return
	}
	res, secrets, err := s.Build(files)
	if err != nil {
		// Build's errors name a file and a line, never the text there.
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid_config", err.Error())
		return
	}
	if dry {
		if err := h.svc.DiffWith(r.Context(), res, secrets, req.Choices); err != nil {
			h.fail(w, s, "dry run", err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, res)
		return
	}

	var userID string
	if id, ok := auth.FromContext(r.Context()); ok {
		userID = id.User.ID
	}
	names := make([]string, 0, len(req.Names))
	for _, role := range s.Roles {
		if n := req.Names[role.Key]; n != "" && files[role.Key] != nil {
			names = append(names, n)
		} else if files[role.Key] != nil {
			names = append(names, role.File)
		}
	}
	applied, err := h.svc.ApplyWith(r.Context(), res, secrets, req.Choices, importer.Meta{UserID: userID, Files: names, Hash: req.PlanHash})
	if errors.Is(err, importer.ErrPlanChanged) {
		fresh, secrets, err := s.Build(files)
		if err == nil {
			err = h.svc.DiffWith(r.Context(), fresh, secrets, req.Choices)
		}
		if err != nil {
			h.fail(w, s, "dry run", err)
			return
		}
		httpx.WriteJSON(w, http.StatusConflict, map[string]any{
			"error":  httpx.ErrorDetail{Code: "plan_changed", Message: "Something changed since the preview. Here's the plan as it is now."},
			"result": fresh,
		})
		return
	}
	if err != nil {
		h.fail(w, s, "import", err)
		return
	}
	created, updated := 0, 0
	for _, c := range applied.Changes {
		switch c.Action {
		case importer.ActionCreate:
			created++
		case importer.ActionUpdate:
			updated++
		}
	}
	h.log.Info(s.ID+" import applied", "import", applied.ImportID, "board", applied.BoardID, "instances", len(applied.InstanceIDs),
		"secrets", res.Plan.SecretsMoved, "created", created, "updated", updated)
	target := "board:" + applied.BoardID
	if applied.BoardID == "" {
		target = "import:" + applied.ImportID
	}
	h.audit.Change(r, "import."+s.ID, target, map[string]any{
		"import_id": applied.ImportID, "board_id": applied.BoardID, "instance_ids": applied.InstanceIDs,
		"created": created, "updated": updated, "secrets_moved": res.Plan.SecretsMoved,
		"skipped": len(req.Choices.Skip), "target": req.Choices.Target,
	}, authz.T1, nil)
	httpx.WriteJSON(w, http.StatusOK, applied)
}

func (h *handler) fail(w http.ResponseWriter, s Source, what string, err error) {
	if errors.Is(err, importer.ErrInvalidChoices) {
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid_choices", strings.TrimPrefix(err.Error(), "importer: invalid choices: "))
		return
	}
	h.internal(w, s.ID+" "+what+" failed", err)
}

func (h *handler) record(w http.ResponseWriter, r *http.Request, s Source) {
	rec, err := h.svc.LoadRecord(r.Context(), s.ID)
	if err != nil {
		h.internal(w, "reading the "+s.ID+" import record failed", err)
		return
	}
	if rec == nil {
		httpx.Error(w, http.StatusNotFound, "not_found", "nothing has been imported from "+s.Name+" yet")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]*importer.Record{"record": rec})
}

// ── History and Revert ──────────────────────────────────────────────────────

func (h *handler) history(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.History(r.Context(), 200)
	if err != nil {
		h.internal(w, "reading the import history failed", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"imports": list})
}

func (h *handler) entry(w http.ResponseWriter, r *http.Request) {
	e, err := h.svc.Entry(r.Context(), r.PathValue("id"))
	if errors.Is(err, importer.ErrNoImport) {
		httpx.Error(w, http.StatusNotFound, "not_found", "no such import")
		return
	}
	if err != nil {
		h.internal(w, "reading an import failed", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, e)
}

func (h *handler) revert(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	dry := r.URL.Query().Get("dry_run") == "1"
	plan, err := h.svc.Revert(r.Context(), id, true)
	if err == nil && !dry && plan.Connections {
		// Removing connections is destructive: the same check as T3.
		actor, _ := authz.ActorFrom(r)
		if authz.WriteRefusal(w, authz.Decide(actor, authz.Request{Scope: "import.revert", Tier: authz.T3}, authz.Env{Now: time.Now()})) {
			return
		}
	}
	if err == nil && !dry {
		plan, err = h.svc.Revert(r.Context(), id, false)
	}
	switch {
	case errors.Is(err, importer.ErrNoImport):
		httpx.Error(w, http.StatusNotFound, "not_found", "no such import")
		return
	case errors.Is(err, importer.ErrReverted):
		httpx.Error(w, http.StatusConflict, "reverted", "This import was reverted already.")
		return
	case err != nil:
		h.internal(w, "reverting an import failed", err)
		return
	}
	if !dry {
		h.audit.Change(r, "import.revert", "import:"+id, map[string]any{"import_id": id, "removed": len(plan.Remove), "kept": len(plan.Keep)}, authz.T3, nil)
	}
	httpx.WriteJSON(w, http.StatusOK, plan)
}

func (h *handler) internal(w http.ResponseWriter, what string, err error) {
	h.log.Error(what, "err", err)
	httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
}
