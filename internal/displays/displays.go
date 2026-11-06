// Package displays serves wall tablets and TVs (v0.5, wave 33;
// docs/plan/15-v0.5.md, "Wave 33: how displays work"): admins make and
// revoke displays in Settings → Displays, and a display reads its one board
// through its own routes under /api/display. internal/auth stores displays
// and checks their secrets; internal/authz's table decides who may call
// each route; internal/server narrows the display's socket to its board.
package displays

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/boards"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/icons"
	"dash.cha.sh/internal/msg"
)

// Service serves the displays routes.
type Service struct {
	Auth   *auth.Service
	Boards *boards.Service
	// Icons serves tiles' icons on the display's own route; nil answers 404.
	Icons *icons.Service
	// Socket is the displays' realtime hub (GET /api/display/ws).
	Socket http.Handler
	// Audit records making, changing and revoking displays.
	Audit *authz.Recorder
	Log   *slog.Logger
	// Now is the hub's clock (time.Now when nil).
	Now func() time.Time
	// Zone is the hub's time zone's name ("" when it isn't known; then
	// the offset stands in). Nil reads it from the process (HubZone).
	Zone func() string
}

// Register mounts:
//
//	GET    /api/displays           → 200 {"displays": [display]}, not revoked, newest first (admin)
//	POST   /api/displays           {name, board, quiet?} → 201 {"display", "secret"}: the secret, once (admin)
//	PATCH  /api/displays/{id}      {name?, quiet? (null clears)} → 200 {"display"} (admin)
//	DELETE /api/displays/{id}      → 204, revoked at once; its sockets close (admin)
//	POST   /api/displays/{id}/link → 200 {"display", "secret"}: a new link's claim code, once; the device on the old one keeps working until it's claimed (admin)
//	POST   /api/display/claim      {token} → 204 + the display cookie: the claim code, once, for the device secret
//	GET    /api/display            → 200 the display, its board, layout, icons and the hub's zone (display)
//	GET    /api/display/ws         → the display's socket (display)
//	GET    /api/display/icons/{id} → a tile's icon (display)
//
// session attaches the signed-in identity on the admin routes; Guard puts
// the display's on the display routes.
func (s *Service) Register(mux *http.ServeMux, session func(http.Handler) http.Handler) {
	if s.Log == nil {
		s.Log = slog.New(slog.DiscardHandler)
	}
	mux.Handle("GET /api/displays", session(http.HandlerFunc(s.list)))
	mux.Handle("POST /api/displays", session(http.HandlerFunc(s.create)))
	mux.Handle("PATCH /api/displays/{id}", session(http.HandlerFunc(s.update)))
	mux.Handle("DELETE /api/displays/{id}", session(http.HandlerFunc(s.revoke)))
	mux.Handle("POST /api/displays/{id}/link", session(http.HandlerFunc(s.link)))
	mux.HandleFunc("POST /api/display/claim", func(w http.ResponseWriter, r *http.Request) {
		if id, ok := s.Auth.HandleDisplayClaim(w, r); ok {
			s.Audit.ChangeByDisplay(r, id, "displays.claim", "display:"+id.Display.ID, nil, authz.T1)
		}
	})
	mux.HandleFunc("GET /api/display", s.read)
	socket := s.Socket
	if socket == nil {
		socket = http.NotFoundHandler()
	}
	mux.Handle("GET /api/display/ws", socket)
	mux.HandleFunc("GET /api/display/icons/{id}", func(w http.ResponseWriter, r *http.Request) {
		if s.Icons == nil {
			httpx.Say(w, http.StatusNotFound, "not_found", msg.New("icons.not_found"))
			return
		}
		s.Icons.Serve(w, r, s.Log)
	})
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) internal(w http.ResponseWriter, what string, err error) {
	s.Log.Error(what, "err", err)
	httpx.Say(w, http.StatusInternalServerError, "internal", msg.New("hub.internal_error"))
}

// View is a display as the API shows it; times are Unix milliseconds.
type View struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	Board      boards.Board `json:"board"`
	Quiet      *auth.Quiet  `json:"quiet"`
	Style      auth.Style   `json:"style"`
	CreatedAt  int64        `json:"created_at"`
	CreatedBy  string       `json:"created_by"`
	LastSeenAt *int64       `json:"last_seen_at"`
	LastSeenIP *string      `json:"last_seen_ip"`
	// LinkExpiresAt is when its unclaimed link stops working; null once
	// claimed.
	LinkExpiresAt *int64 `json:"link_expires_at"`
}

func view(d auth.Display) View {
	v := View{ID: d.ID, Name: d.Name, Board: boards.Board{ID: d.BoardID, Name: d.BoardName}, Quiet: d.Quiet, Style: d.Style,
		CreatedAt: d.CreatedAt.UnixMilli(), CreatedBy: d.CreatedBy}
	if d.LastSeenAt != nil {
		ms := d.LastSeenAt.UnixMilli()
		v.LastSeenAt = &ms
	}
	if d.LinkExpiresAt != nil {
		ms := d.LinkExpiresAt.UnixMilli()
		v.LinkExpiresAt = &ms
	}
	if d.LastSeenIP != "" {
		ip := d.LastSeenIP
		v.LastSeenIP = &ip
	}
	return v
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	list, err := s.Auth.ListDisplays(r.Context())
	if err != nil {
		s.internal(w, "listing displays failed", err)
		return
	}
	out := make([]View, 0, len(list))
	for _, d := range list {
		out = append(out, view(d))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string][]View{"displays": out})
}

// CreateRequest is POST /api/displays' body.
type CreateRequest struct {
	Name  string `json:"name"`
	Board string `json:"board"`
	// Style is wall (the default), tv or ink.
	Style auth.Style  `json:"style"`
	Quiet *auth.Quiet `json:"quiet"`
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	a, _ := authz.ActorFrom(r)
	var req CreateRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	say := func(status int, code string, m msg.M) {
		s.Audit.Refuse(r, "displays.create", "", map[string]any{"name": clip(req.Name), "board": clip(req.Board)}, authz.T1, code, m.Text())
		httpx.Say(w, status, code, m)
	}
	d, secret, err := s.Auth.CreateDisplay(r.Context(), a.ID, req.Name, req.Board, req.Style, req.Quiet)
	var ve *auth.ValidationError
	switch {
	case errors.As(err, &ve):
		say(http.StatusUnprocessableEntity, "invalid", ve.M)
		return
	case errors.Is(err, auth.ErrNoSuchBoard):
		say(http.StatusUnprocessableEntity, "invalid", msg.New("displays.no_board"))
		return
	case err != nil:
		s.internal(w, "creating a display failed", err)
		return
	}
	s.Audit.Change(r, "displays.create", "display:"+d.ID, map[string]any{"name": d.Name, "board": d.BoardID, "style": d.Style, "quiet": d.Quiet}, authz.T1, nil)
	// The only response that ever carries the secret; /api/displays is
	// never compressed (server.noCompress).
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"display": view(d), "secret": secret})
}

func (s *Service) update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var raw map[string]json.RawMessage
	if !httpx.DecodeJSON(w, r, &raw) {
		return
	}
	var name *string
	if v, ok := raw["name"]; ok {
		var n string
		if json.Unmarshal(v, &n) != nil {
			httpx.Say(w, http.StatusUnprocessableEntity, "invalid", msg.New("displays.name"))
			return
		}
		name = &n
	}
	var style *auth.Style
	if v, ok := raw["style"]; ok {
		var st auth.Style
		if json.Unmarshal(v, &st) != nil {
			httpx.Say(w, http.StatusUnprocessableEntity, "invalid", msg.New("displays.style"))
			return
		}
		style = &st
	}
	var quiet *auth.Quiet
	v, setQuiet := raw["quiet"]
	if setQuiet && string(v) != "null" {
		quiet = &auth.Quiet{}
		if json.Unmarshal(v, quiet) != nil {
			httpx.Say(w, http.StatusUnprocessableEntity, "invalid", msg.New("displays.quiet"))
			return
		}
	}
	d, err := s.Auth.UpdateDisplay(r.Context(), id, name, style, setQuiet, quiet)
	var ve *auth.ValidationError
	switch {
	case errors.As(err, &ve):
		httpx.Say(w, http.StatusUnprocessableEntity, "invalid", ve.M)
		return
	case errors.Is(err, auth.ErrDisplayNotFound):
		httpx.Say(w, http.StatusNotFound, "not_found", msg.New("displays.not_found"))
		return
	case err != nil:
		s.internal(w, "changing a display failed", err)
		return
	}
	params := map[string]any{}
	if name != nil {
		params["name"] = d.Name
	}
	if style != nil {
		params["style"] = d.Style
	}
	if setQuiet {
		params["quiet"] = d.Quiet
	}
	s.Audit.Change(r, "displays.update", "display:"+d.ID, params, authz.T1, nil)
	httpx.WriteJSON(w, http.StatusOK, map[string]View{"display": view(d)})
}

func (s *Service) link(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d, code, err := s.Auth.NewDisplayLink(r.Context(), id)
	switch {
	case errors.Is(err, auth.ErrDisplayNotFound):
		s.Audit.Refuse(r, "displays.link", "display:"+clip(id), nil, authz.T1, "not_found", msg.New("displays.not_found").Text())
		httpx.Say(w, http.StatusNotFound, "not_found", msg.New("displays.not_found"))
		return
	case err != nil:
		s.internal(w, "making a display's link failed", err)
		return
	}
	s.Audit.Change(r, "displays.link", "display:"+d.ID, map[string]any{"name": d.Name}, authz.T1, nil)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"display": view(d), "secret": code})
}

func (s *Service) revoke(w http.ResponseWriter, r *http.Request) {
	a, _ := authz.ActorFrom(r)
	id := r.PathValue("id")
	d, changed, err := s.Auth.RevokeDisplay(r.Context(), id, a.ID)
	switch {
	case errors.Is(err, auth.ErrDisplayNotFound) || (err == nil && !changed):
		s.Audit.Refuse(r, "displays.revoke", "display:"+clip(id), nil, authz.T1, "not_found", msg.New("displays.not_found").Text())
		httpx.Say(w, http.StatusNotFound, "not_found", msg.New("displays.not_found"))
		return
	case err != nil:
		s.internal(w, "revoking a display failed", err)
		return
	}
	s.Audit.Change(r, "displays.revoke", "display:"+d.ID, map[string]any{"name": d.Name}, authz.T1, nil)
	w.WriteHeader(http.StatusNoContent)
}

// Read is GET /api/display's answer: what a display needs to lay out its
// board and keep the hub's time.
type Read struct {
	Display struct {
		ID    string      `json:"id"`
		Name  string      `json:"name"`
		Quiet *auth.Quiet `json:"quiet"`
		Style auth.Style  `json:"style"`
	} `json:"display"`
	Board  boards.Board `json:"board"`
	Layout struct {
		Revision      int64           `json:"revision"`
		FormatVersion int64           `json:"format_version"`
		Layout        json.RawMessage `json:"layout"`
		Tiles         json.RawMessage `json:"tiles,omitempty"`
	} `json:"layout"`
	// Icons is icons.Map with the display's own icon route.
	Icons icons.Map `json:"icons"`
	// Zone is the hub's IANA zone, "" when unknown; OffsetMinutes is its
	// offset from UTC now, east positive.
	Zone          string `json:"zone"`
	OffsetMinutes int    `json:"offset_minutes"`
	Now           int64  `json:"now"`
}

func (s *Service) read(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.FromContext(r.Context())
	if !ok || id.Display == nil {
		httpx.Say(w, http.StatusUnauthorized, "invalid_display", msg.New("displays.signed_out"))
		return
	}
	d, err := s.Auth.GetDisplay(r.Context(), id.Display.ID)
	if err != nil {
		s.internal(w, "reading a display failed", err)
		return
	}
	l, err := s.Boards.Layout(r.Context(), d.BoardID)
	if err != nil {
		s.internal(w, "reading a display's layout failed", err)
		return
	}
	var out Read
	out.Display.ID, out.Display.Name, out.Display.Quiet, out.Display.Style = d.ID, d.Name, d.Quiet, d.Style
	out.Board = boards.Board{ID: d.BoardID, Name: d.BoardName}
	out.Layout.Revision, out.Layout.FormatVersion, out.Layout.Tiles = l.Revision, l.FormatVersion, l.Tiles
	out.Layout.Layout = l.Document
	if out.Layout.Layout == nil {
		out.Layout.Layout = json.RawMessage("null")
	}
	out.Icons = icons.Map{ByIcon: map[string]string{}, ByOrigin: map[string]string{}, ByInstance: map[string]string{}}
	if s.Icons != nil {
		m, err := s.Icons.Map(r.Context())
		if err != nil {
			s.Log.Warn("listing icons for a display failed", "err", err)
		} else {
			out.Icons = DisplayIcons(m)
		}
	}
	now := s.now()
	zone := s.Zone
	if zone == nil {
		zone = HubZone
	}
	out.Zone = zone()
	_, off := now.Zone()
	out.OffsetMinutes = off / 60
	out.Now = now.UnixMilli()
	httpx.WriteJSON(w, http.StatusOK, out)
}

// DisplayIcons is m with each path on the display's icon route.
func DisplayIcons(m icons.Map) icons.Map {
	move := func(in map[string]string) map[string]string {
		out := make(map[string]string, len(in))
		for k, p := range in {
			out[k] = strings.Replace(p, "/api/icons/", "/api/display/icons/", 1)
		}
		return out
	}
	return icons.Map{ByIcon: move(m.ByIcon), ByOrigin: move(m.ByOrigin), ByInstance: move(m.ByInstance)}
}

// HubZone is the hub's IANA time zone: TZ when it names one, time.Local's
// name, or what /etc/localtime links to; "" when none says.
func HubZone() string {
	if tz, ok := os.LookupEnv("TZ"); ok {
		tz = strings.TrimPrefix(tz, ":")
		if _, err := time.LoadLocation(tz); err == nil && tz != "" && !strings.HasPrefix(tz, "/") {
			return tz
		}
	}
	if n := time.Local.String(); n != "Local" && n != "" {
		return n
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if _, zone, ok := strings.Cut(filepath.ToSlash(target), "zoneinfo/"); ok {
			if _, err := time.LoadLocation(zone); err == nil {
				return zone
			}
		}
	}
	return ""
}

func clip(s string) string {
	if len(s) > 100 {
		return s[:100]
	}
	return s
}
