package server

// Dozzle deep links (docs/plan/14-v0.4.md, "Reach"): the address of a Dozzle
// instance the household runs, so a container's sheet can open its logs
// there. Cha Dash never talks to Dozzle: the browser follows the link, and
// Dozzle asks for its own sign-in if it has one. Dozzle's routes put a
// container at <base>/container/<id>, by its short (12-character) ID, which
// the Docker integration publishes as attrs.container_id.
//
// It's the settings row DozzleKey, {"url": "…"}; missing is off.
//
//	GET /api/settings/dozzle   {"url"}: "" when unset (viewer)
//	PUT /api/settings/dozzle   {"url"} → the same; "" turns it off (admin; audited as settings.dozzle.update)

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/msg"
	"dash.cha.sh/internal/store"
)

// DozzleKey is the settings row the Dozzle address lives in.
const DozzleKey = "dozzle"

// DozzleSettings is the Dozzle address as the row and the API hold it.
type DozzleSettings struct {
	// URL is Dozzle's base address, http(s) with no user, query or
	// fragment, without a trailing slash: "https://logs.home.example" or
	// "http://10.20.30.5:8080/dozzle". Empty: no links.
	URL string `json:"url"`
}

// CleanDozzleURL checks and normalises a Dozzle base address: "" stays "",
// else http or https with a host, no user, query or fragment, and no
// trailing slash.
func CleanDozzleURL(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", true
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", false
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), true
}

func readDozzle(ctx context.Context, st *store.Store) (DozzleSettings, error) {
	var raw string
	err := st.Reader().QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, DozzleKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return DozzleSettings{}, nil
	}
	if err != nil {
		return DozzleSettings{}, err
	}
	var d DozzleSettings
	if json.Unmarshal([]byte(raw), &d) != nil {
		return DozzleSettings{}, nil // unreadable: no links
	}
	if clean, ok := CleanDozzleURL(d.URL); ok {
		d.URL = clean
	} else {
		d.URL = ""
	}
	return d, nil
}

func registerDozzle(mux *http.ServeMux, wrap func(http.Handler) http.Handler, st *store.Store, rec *authz.Recorder, log *slog.Logger) {
	mux.Handle("GET /api/settings/dozzle", wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d, err := readDozzle(r.Context(), st)
		if err != nil {
			log.Error("reading the Dozzle setting failed", "err", err)
			httpx.Say(w, http.StatusInternalServerError, "internal", msg.New("hub.internal_error"))
			return
		}
		httpx.WriteJSON(w, http.StatusOK, d)
	})))
	mux.Handle("PUT /api/settings/dozzle", wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			URL *string `json:"url"`
		}
		if !httpx.DecodeJSON(w, r, &req) {
			return
		}
		if req.URL == nil {
			httpx.Say(w, http.StatusUnprocessableEntity, "invalid", msg.New("dozzle.no_url"))
			return
		}
		clean, ok := CleanDozzleURL(*req.URL)
		if !ok {
			httpx.Say(w, http.StatusUnprocessableEntity, "invalid", msg.New("dozzle.bad_url"))
			return
		}
		before, err := readDozzle(r.Context(), st)
		if err == nil {
			b, _ := json.Marshal(DozzleSettings{URL: clean})
			_, err = st.Writer().ExecContext(r.Context(), `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
				ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
				DozzleKey, string(b), time.Now().UnixMilli())
		}
		if err != nil {
			log.Error("saving the Dozzle setting failed", "err", err)
			httpx.Say(w, http.StatusInternalServerError, "internal", msg.New("hub.internal_error"))
			return
		}
		rec.Change(r, "settings.dozzle.update", "settings:dozzle", map[string]string{"before": before.URL, "url": clean}, authz.T1, nil)
		httpx.WriteJSON(w, http.StatusOK, DozzleSettings{URL: clean})
	})))
}
