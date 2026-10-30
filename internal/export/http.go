package export

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/httpx"
)

// MaxBodyBytes bounds an import request: a Home file, JSON-escaped, and a
// passphrase.
const MaxBodyBytes = MaxFileBytes + MaxFileBytes/2

// Register mounts the export and import API, each route wrapped by wrap
// (the hub's owner-session check):
//
//	GET  /api/export                     the Home file, placeholders only (application/yaml, as an attachment)
//	POST /api/export                     {passphrase} → the same with the encrypted secrets bundle
//	POST /api/import/chadash?dry_run=1   {yaml, passphrase?} → 200 {plan}
//	POST /api/import/chadash             the same body → 200 {plan, changed, board_ids, instance_ids}
//
// Errors: 422 invalid_export (the message names a line and a path, never a
// value), weak_passphrase, wrong_passphrase, bad_bundle; 409
// secret_in_plain_text when an export would leak a value. Secret values and
// passphrases never appear in a response or a log.
func Register(mux *http.ServeMux, wrap func(http.Handler) http.Handler, svc *Service, log *slog.Logger) {
	h := handler{svc: svc, log: log}
	mux.Handle("GET /api/export", wrap(http.HandlerFunc(h.exportPlain)))
	mux.Handle("POST /api/export", wrap(http.HandlerFunc(h.exportBundle)))
	mux.Handle("POST /api/import/chadash", wrap(http.HandlerFunc(h.importFile)))
}

type handler struct {
	svc *Service
	log *slog.Logger
}

func (h handler) exportPlain(w http.ResponseWriter, r *http.Request) {
	h.export(w, r, "")
}

type exportRequest struct {
	Passphrase string `json:"passphrase"`
}

func (h handler) exportBundle(w http.ResponseWriter, r *http.Request) {
	var req exportRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if err := CheckPassphrase(req.Passphrase); err != nil {
		httpx.Error(w, http.StatusUnprocessableEntity, "weak_passphrase", "the passphrase needs at least 12 characters")
		return
	}
	h.export(w, r, req.Passphrase)
}

func (h handler) export(w http.ResponseWriter, r *http.Request, passphrase string) {
	out, err := h.svc.Export(r.Context(), passphrase)
	var leak *LeakError
	switch {
	case errors.As(err, &leak):
		h.log.Warn("export refused: a secret value appears in plain text", "secret", leak.Secret)
		httpx.Error(w, http.StatusConflict, "secret_in_plain_text", strings.TrimPrefix(leak.Error(), "export: "))
		return
	case errors.Is(err, ErrNoVault):
		httpx.Error(w, http.StatusServiceUnavailable, "no_vault", "the vault isn't available")
		return
	case err != nil:
		h.internal(w, "export failed", err)
		return
	}
	h.log.Info("exported the hub", "boards", out.Boards, "instances", out.Instances, "settings", out.Settings,
		"placeholders", out.Secrets, "bundled", out.Bundled, "missing", len(out.Missing))
	// An export carries the hub's whole configuration, and with a
	// passphrase its secrets too: who took one is worth keeping.
	h.svc.Audit.Change(r, "export.home", "hub", map[string]any{
		"boards": out.Boards, "instances": out.Instances, "settings": out.Settings,
		"placeholders": out.Secrets, "bundled": out.Bundled,
	}, authz.T0, nil)
	name := "chadash-home-" + h.svc.now().UTC().Format("2006-01-02")
	if out.Bundled > 0 || passphrase != "" {
		name += "-with-secrets"
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.yaml"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out.Data)
}

type importRequest struct {
	YAML       string `json:"yaml"`
	Passphrase string `json:"passphrase"`
}

func (h handler) importFile(w http.ResponseWriter, r *http.Request) {
	var req importRequest
	if !httpx.DecodeJSONLimit(w, r, &req, MaxBodyBytes) {
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
	if strings.TrimSpace(req.YAML) == "" {
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid", "yaml is required: the Home file's text")
		return
	}
	d, err := Parse([]byte(req.YAML))
	if err != nil {
		h.importError(w, err)
		return
	}
	// The security settings are the owner's, and the step-up rule needs a
	// step-up (authz decides); anyone else imports the rest of the file.
	actor, _ := authz.ActorFrom(r)
	var refusal error
	ctx := WithSecurityGate(r.Context(), func(stepUp bool) error {
		refusal = authz.ChangeSecuritySettings(actor, stepUp, h.svc.now())
		return refusal
	})
	r = r.WithContext(ctx)
	if dry {
		plan, err := h.svc.Plan(r.Context(), d, req.Passphrase)
		if err != nil {
			h.importError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]*Plan{"plan": plan})
		return
	}
	var userID string
	if id, ok := auth.FromContext(r.Context()); ok {
		userID = id.User.ID
	}
	applied, err := h.svc.Apply(r.Context(), d, req.Passphrase, userID)
	if err != nil {
		h.importError(w, err)
		return
	}
	h.auditSecurity(r, applied.Plan, refusal)
	s := applied.Plan.Summary
	h.svc.Audit.Change(r, "import.chadash", "hub", map[string]any{
		"board_ids": applied.BoardIDs, "instance_ids": applied.InstanceIDs,
		"created": s.Create, "updated": s.Update, "unchanged": s.Unchanged, "stale": s.Stale,
		"secrets_restored": s.SecretsRestored, "secrets_needed": s.SecretsNeeded,
	}, authz.T1, nil)
	h.log.Info("home file imported", "boards", len(applied.BoardIDs), "instances", len(applied.InstanceIDs),
		"created", s.Create, "updated", s.Update, "unchanged", s.Unchanged, "stale", s.Stale,
		"secrets_restored", s.SecretsRestored, "secrets_needed", s.SecretsNeeded)
	httpx.WriteJSON(w, http.StatusOK, applied)
}

// auditSecurity records a security change an import made, or refused, as
// settings.security.update, like Settings → Security does. AfterApply
// lets the running hub take the imported settings and decisions up.
func (h handler) auditSecurity(r *http.Request, p *Plan, refusal error) {
	for _, c := range p.Changes {
		if c.Kind != "security" || (c.Action != actionUpdate && c.Skipped == "") {
			continue
		}
		params := map[string]any{"via": "import"}
		tier := authz.T1
		for _, f := range c.Fields {
			params[strings.TrimPrefix(f.Path, "/")] = f.To
			if f.Path == "/step_up" || strings.HasPrefix(f.Path, "/sign_in") {
				tier = authz.T3
			}
		}
		if c.Skipped != "" {
			h.svc.Audit.Change(r, "settings.security.update", "settings:security", params, tier, refusal)
		} else {
			h.svc.Audit.Change(r, "settings.security.update", "settings:security", params, tier, nil)
		}
	}
	if h.svc.AfterApply != nil && p.Changed() {
		h.svc.AfterApply(r.Context())
	}
}

func (h handler) importError(w http.ResponseWriter, err error) {
	var pe *ParseError
	switch {
	case errors.As(err, &pe):
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid_export", pe.Error())
	case errors.Is(err, ErrWrongPassphrase):
		httpx.Error(w, http.StatusUnprocessableEntity, "wrong_passphrase", "the passphrase is wrong, or the secrets bundle was changed")
	case errors.Is(err, ErrBadBundle):
		httpx.Error(w, http.StatusUnprocessableEntity, "bad_bundle", "the secrets bundle is damaged or uses parameters this hub doesn't support")
	case errors.Is(err, ErrNoVault):
		httpx.Error(w, http.StatusServiceUnavailable, "no_vault", "the vault isn't available")
	default:
		h.internal(w, "home file import failed", err)
	}
}

func (h handler) internal(w http.ResponseWriter, what string, err error) {
	h.log.Error(what, "err", err)
	httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
}
