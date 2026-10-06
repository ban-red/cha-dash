package notify

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/httpx"
)

// Register mounts the notifications API. Who may call each route is
// decided by internal/authz/routes.go: channels, rules and the delivery log
// are admin+ (Settings → Notifications); the VAPID key and your own
// devices are open to every signed-in role. session only attaches the
// identity.
//
//	GET    /api/notify/channels                  every channel, Web Push first; secrets as {set}
//	POST   /api/notify/channels                  {kind, name, config, secrets, enabled?} → 201 channel
//	PUT    /api/notify/channels/{id}             {name?, config?, secrets?, enabled?} → channel
//	DELETE /api/notify/channels/{id}             → 204 (409 for Web Push)
//	POST   /api/notify/channels/{id}/test        send a test now → {ok, status_code, error, devices?}
//	GET    /api/notify/rules                     {rules}
//	PUT    /api/notify/rules/{id}                {enabled?, resolved?, grace_minutes?, channels?} → rule
//	GET    /api/notify/deliveries?state=&limit=  {deliveries}, newest first
//	GET    /api/notify/push/key                  {public_key}: the VAPID key, base64url
//	GET    /api/notify/push/subscriptions        your devices
//	POST   /api/notify/push/subscriptions        PushSubscription.toJSON() + label → 201 device
//	DELETE /api/notify/push/subscriptions/{id}   → 204
//	POST   /api/notify/push/unsubscribe          {endpoint} → 204
func (s *Service) Register(mux *http.ServeMux, session func(http.Handler) http.Handler) {
	mux.Handle("GET /api/notify/channels", session(http.HandlerFunc(s.handleChannels)))
	mux.Handle("POST /api/notify/channels", session(http.HandlerFunc(s.handleCreateChannel)))
	mux.Handle("PUT /api/notify/channels/{id}", session(http.HandlerFunc(s.handleUpdateChannel)))
	mux.Handle("DELETE /api/notify/channels/{id}", session(http.HandlerFunc(s.handleDeleteChannel)))
	mux.Handle("POST /api/notify/channels/{id}/test", session(http.HandlerFunc(s.handleTest)))
	mux.Handle("GET /api/notify/rules", session(http.HandlerFunc(s.handleRules)))
	mux.Handle("PUT /api/notify/rules/{id}", session(http.HandlerFunc(s.handleUpdateRule)))
	mux.Handle("GET /api/notify/deliveries", session(http.HandlerFunc(s.handleDeliveries)))
	mux.Handle("GET /api/notify/push/key", session(http.HandlerFunc(s.handleKey)))
	mux.Handle("GET /api/notify/push/subscriptions", session(http.HandlerFunc(s.handleDevices)))
	mux.Handle("POST /api/notify/push/subscriptions", session(http.HandlerFunc(s.handleSubscribe)))
	mux.Handle("DELETE /api/notify/push/subscriptions/{id}", session(http.HandlerFunc(s.handleRemoveDevice)))
	mux.Handle("POST /api/notify/push/unsubscribe", session(http.HandlerFunc(s.handleUnsubscribe)))
}

// fail writes err as the API's error.
func (s *Service) fail(w http.ResponseWriter, err error, notFound string) {
	var inv *invalidError
	var sen sentence
	switch {
	case errors.As(err, &inv):
		httpx.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": httpx.ErrorDetail{Code: "invalid", Message: inv.Error()}, "fields": inv.fields})
	case errors.Is(err, errNotFound):
		httpx.Error(w, http.StatusNotFound, "not_found", notFound)
	case errors.Is(err, errNameTaken):
		httpx.Error(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, errBuiltin):
		httpx.Error(w, http.StatusConflict, "builtin", err.Error())
	case errors.Is(err, errNoDevices):
		httpx.Error(w, http.StatusConflict, "no_devices", err.Error())
	case errors.Is(err, errLocked):
		httpx.Error(w, http.StatusConflict, "locked", err.Error())
	case errors.Is(err, errUnknownRule):
		httpx.Error(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, errNoVault):
		httpx.Error(w, http.StatusServiceUnavailable, "unavailable", "The hub has no vault, so it can't keep a channel's secrets or a Web Push key.")
	case errors.As(err, &sen):
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid", sen.Error())
	default:
		s.log.Error("notify: request failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

// ── channels ─────────────────────────────────────────────────────────────

func (s *Service) handleChannels(w http.ResponseWriter, r *http.Request) {
	cs, err := s.Channels(r.Context())
	if err != nil {
		s.fail(w, err, "")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"channels": cs, "public_url": s.publicURL != "", "web_push": s.vault != nil})
}

// auditParams are what the audit log keeps about a channel change: never a
// secret, and only the host of a URL (a webhook URL often is a credential).
func auditParams(c Channel, secrets []string) map[string]any {
	p := map[string]any{"kind": c.Kind, "name": c.Name, "enabled": c.Enabled}
	var cfg struct {
		Server string `json:"server"`
		URL    string `json:"url"`
	}
	_ = json.Unmarshal(c.Config, &cfg)
	for _, raw := range []string{cfg.Server, cfg.URL} {
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			p["host"] = u.Host
		}
	}
	if len(secrets) > 0 {
		sort.Strings(secrets)
		p["secrets_changed"] = secrets
	}
	return p
}

func secretKeys(m map[string]*string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func (s *Service) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	var in ChannelInput
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	c, err := s.CreateChannel(r.Context(), in)
	if err != nil {
		s.fail(w, err, "")
		return
	}
	s.Audit.Change(r, "settings.notify.channel.create", "notify:channel/"+c.ID, auditParams(c, secretKeys(in.Secrets)), authz.T1, nil)
	httpx.WriteJSON(w, http.StatusCreated, c)
}

func (s *Service) handleUpdateChannel(w http.ResponseWriter, r *http.Request) {
	var in ChannelInput
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	c, changed, err := s.UpdateChannel(r.Context(), r.PathValue("id"), in)
	if err != nil {
		s.fail(w, err, "There's no such channel.")
		return
	}
	s.Audit.Change(r, "settings.notify.channel.update", "notify:channel/"+c.ID, auditParams(c, changed), authz.T1, nil)
	httpx.WriteJSON(w, http.StatusOK, c)
}

func (s *Service) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	c, err := s.DeleteChannel(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err, "There's no such channel.")
		return
	}
	s.Audit.Change(r, "settings.notify.channel.delete", "notify:channel/"+c.ID, auditParams(c, nil), authz.T1, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleTest(w http.ResponseWriter, r *http.Request) {
	a := s.actorOf(r)
	res, err := s.Test(r.Context(), r.PathValue("id"), a.ID, a.Name)
	if err != nil {
		s.fail(w, err, "There's no such channel.")
		return
	}
	s.Audit.Change(r, "settings.notify.channel.test", "notify:channel/"+r.PathValue("id"), map[string]any{"ok": res.OK}, authz.T0, nil)
	httpx.WriteJSON(w, http.StatusOK, res)
}

// ── rules and the log ────────────────────────────────────────────────────

func (s *Service) handleRules(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"rules": s.Rules()})
}

func (s *Service) handleUpdateRule(w http.ResponseWriter, r *http.Request) {
	var in RuleUpdate
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	a := s.actorOf(r)
	rule, err := s.UpdateRule(r.Context(), r.PathValue("id"), in, a.Name)
	if err != nil {
		s.fail(w, err, "There's no such rule.")
		return
	}
	s.Audit.Change(r, "settings.notify.rule.update", "notify:rule/"+rule.ID, rule.RuleSettings, authz.T1, nil)
	httpx.WriteJSON(w, http.StatusOK, rule)
}

func (s *Service) handleDeliveries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	if state != "" && !deliveryStates[state] {
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid", "state is pending, sent, failed or dropped.")
		return
	}
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			httpx.Error(w, http.StatusUnprocessableEntity, "invalid", "limit is 1 to 200.")
			return
		}
		limit = n
	}
	ds, err := s.Deliveries(r.Context(), state, limit)
	if err != nil {
		s.fail(w, err, "")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"deliveries": ds})
}

// ── your devices ─────────────────────────────────────────────────────────

func (s *Service) handleKey(w http.ResponseWriter, r *http.Request) {
	k, err := s.vapid(r.Context())
	if err != nil {
		s.fail(w, err, "")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"public_key": base64.RawURLEncoding.EncodeToString(k.pub)})
}

func (s *Service) handleDevices(w http.ResponseWriter, r *http.Request) {
	a := s.actorOf(r)
	ds, err := s.Devices(r.Context(), a.ID)
	if err != nil {
		s.fail(w, err, "")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"subscriptions": ds})
}

func (s *Service) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	var in SubscribeInput
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	if in.Label == "" {
		in.Label = labelFromUA(r.UserAgent())
	}
	a := s.actorOf(r)
	d, err := s.Subscribe(r.Context(), a.ID, in)
	if err != nil {
		s.fail(w, err, "")
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, d)
}

func (s *Service) handleRemoveDevice(w http.ResponseWriter, r *http.Request) {
	a := s.actorOf(r)
	if err := s.RemoveDevice(r.Context(), a.ID, r.PathValue("id")); err != nil {
		s.fail(w, err, "There's no such device on your account.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleUnsubscribe(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Endpoint string `json:"endpoint"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	a := s.actorOf(r)
	if err := s.Unsubscribe(r.Context(), a.ID, in.Endpoint); err != nil {
		s.fail(w, err, "That device isn't subscribed on your account.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// actorOf is who's asking: authz.ActorFrom, or a stand-in in tests.
func (s *Service) actorOf(r *http.Request) authz.Actor {
	if s.actor != nil {
		return s.actor(r)
	}
	a, _ := authz.ActorFrom(r)
	return a
}

// labelFromUA names a device from its browser and system, roughly:
// "Firefox on Android". Only for showing in the device list.
func labelFromUA(ua string) string {
	browser := ""
	switch {
	case strings.Contains(ua, "Edg/"):
		browser = "Edge"
	case strings.Contains(ua, "Firefox/"):
		browser = "Firefox"
	case strings.Contains(ua, "Chrome/"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	}
	system := ""
	switch {
	case strings.Contains(ua, "Android"):
		system = "Android"
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"):
		system = "iOS"
	case strings.Contains(ua, "Mac OS X"), strings.Contains(ua, "Macintosh"):
		system = "macOS"
	case strings.Contains(ua, "Windows"):
		system = "Windows"
	case strings.Contains(ua, "Linux"):
		system = "Linux"
	}
	switch {
	case browser != "" && system != "":
		return browser + " on " + system
	case browser != "":
		return browser
	}
	return system
}
