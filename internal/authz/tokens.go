package authz

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/msg"
)

// Token lifetimes: every token expires, 90 days unless the person picks
// another number of days, at most a year.
const (
	DefaultTokenDays = 90
	MaxTokenDays     = 365
	// MaxTokenScopes bounds a token's scope list.
	MaxTokenScopes = 50
)

// TokenAction is an action a token could be scoped to, for the scope picker.
type TokenAction struct {
	ID    string
	Title string
	Tier  Tier
	// Household and ReadsData are the Def's marks (see Request).
	Household, ReadsData bool
}

// Tokens serves Settings → API tokens. Every signed-in role manages its own
// tokens; the owner sees and may revoke everyone's, admins everyone's but the owner's. The routes
// are session only (Guard refuses tokens), so a token can't mint, list or
// revoke tokens. auth stores them; the rules here are decisions: which
// scopes a role may hand a token, how long one may live, whose tokens a
// person sees.
type Tokens struct {
	auth *auth.Service
	log  *slog.Logger
	now  func() time.Time
	// Audit records creating and revoking tokens (nil: nothing).
	Audit *Recorder
	// Actions lists the registered actions, for the scope picker. Nil
	// offers read scopes and "*" only.
	Actions func() []TokenAction
	// OnRevoke runs after a token is revoked, so what it was waiting on is
	// withdrawn (the engine's approvals). Nil does nothing.
	OnRevoke func(ctx context.Context, tokenID string)
	// AgentsOn reports whether the owner has turned Agents on, which
	// minting an agent token needs (security.Service.AgentsOn). Nil: off.
	AgentsOn func(ctx context.Context) (bool, error)
}

// NewTokens returns the tokens API over a.
func NewTokens(a *auth.Service, log *slog.Logger) *Tokens {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Tokens{auth: a, log: log, now: time.Now}
}

// Register mounts:
//
//	GET    /api/tokens          → 200 {"tokens": [token]}: your own; the owner, everyone's; admins, everyone's but the owner's
//	POST   /api/tokens          {name, scopes, kind?, expires_in_days?} → 201 {"token", "secret"}: the secret, once
//	DELETE /api/tokens/{id}     → 200 {"token"}, revoked at once; your own, or any you can see (above; 404 otherwise)
//	GET    /api/tokens/scopes   → 200 the scope vocabulary for the picker, per your role
//
// wrap attaches the session; Guard admits every signed-in role and no token.
func (t *Tokens) Register(mux *http.ServeMux, wrap func(http.Handler) http.Handler) {
	mux.Handle("GET /api/tokens", wrap(http.HandlerFunc(t.list)))
	mux.Handle("POST /api/tokens", wrap(http.HandlerFunc(t.create)))
	mux.Handle("DELETE /api/tokens/{id}", wrap(http.HandlerFunc(t.revoke)))
	mux.Handle("GET /api/tokens/scopes", wrap(http.HandlerFunc(t.scopes)))
}

// seesAllTokens reports whether role sees and revokes everyone's tokens.
func seesAllTokens(role string) bool { return rank(role) >= rank(auth.RoleAdmin) }

func (t *Tokens) list(w http.ResponseWriter, r *http.Request) {
	a, _ := ActorFrom(r)
	user := a.ID
	if seesAllTokens(a.Role) {
		user = ""
	}
	list, err := t.auth.ListTokens(r.Context(), user)
	if err != nil {
		t.internal(w, "listing API tokens failed", err)
		return
	}
	if user == "" && a.Role != auth.RoleOwner {
		// An admin sees everyone's tokens but the owner's, as they manage
		// every account but the owner's.
		owner, err := t.ownerID(r.Context())
		if err != nil {
			t.internal(w, "reading the owner failed", err)
			return
		}
		list = slices.DeleteFunc(list, func(tok auth.APIToken) bool { return tok.UserID == owner })
	}
	httpx.WriteJSON(w, http.StatusOK, map[string][]auth.APIToken{"tokens": list})
}

// ownerID is the owner's user ID ("" before setup).
func (t *Tokens) ownerID(ctx context.Context) (string, error) {
	users, err := t.auth.ListUsers(ctx)
	if err != nil {
		return "", err
	}
	for _, u := range users {
		if u.Role == auth.RoleOwner {
			return u.ID, nil
		}
	}
	return "", nil
}

// visible reports whether a may see and revoke tok: their own; anyone's
// for an admin, except the owner's, which only the owner sees.
func (t *Tokens) visible(ctx context.Context, a Actor, tok auth.APIToken) (bool, error) {
	switch {
	case tok.UserID == a.ID || a.Role == auth.RoleOwner:
		return true, nil
	case !seesAllTokens(a.Role):
		return false, nil
	}
	owner, err := t.ownerID(ctx)
	return tok.UserID != owner, err
}

func (t *Tokens) create(w http.ResponseWriter, r *http.Request) {
	a, _ := ActorFrom(r)
	var req struct {
		Name          string   `json:"name"`
		Kind          string   `json:"kind"`
		Scopes        []string `json:"scopes"`
		ExpiresInDays *int     `json:"expires_in_days"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	days := DefaultTokenDays
	if req.ExpiresInDays != nil {
		days = *req.ExpiresInDays
	}
	// Refusals are audited too: asking for a scope your role can't hand
	// out is worth seeing. Only the name and the scopes asked for.
	audit := func(code, text string) {
		t.Audit.Refuse(r, "tokens.create", "", map[string]any{"name": clipString(req.Name, 100), "kind": clipString(req.Kind, 20), "scopes": clipScopes(req.Scopes)}, T1, code, text)
	}
	// Scope and validation sentences aren't in the catalog yet.
	refuse := func(text string) {
		audit("invalid", text)
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid", text)
	}
	say := func(status int, code string, m msg.M) {
		audit(code, m.Text())
		httpx.Say(w, status, code, m)
	}
	kind, ok := auth.ParseTokenKind(req.Kind)
	if !ok {
		say(http.StatusUnprocessableEntity, "invalid", msg.New("authz.token_kind"))
		return
	}
	if kind == auth.KindAgent {
		// An agent token needs Agents on (ADR-0017), and holds the agent
		// vocabulary (agents.go): reads, narrowable, and propose:<action>.
		on := false
		if t.AgentsOn != nil {
			var err error
			if on, err = t.AgentsOn(r.Context()); err != nil {
				t.internal(w, "reading the Agents setting failed", err)
				return
			}
		}
		if !on {
			say(http.StatusForbidden, "agents_off", msg.New("authz.agents_off"))
			return
		}
	}
	if days < 1 || days > MaxTokenDays {
		say(http.StatusUnprocessableEntity, "invalid", msg.New("authz.token_days"))
		return
	}
	var scopes []string
	var err error
	if kind == auth.KindAgent {
		scopes, err = cleanAgentScopes(req.Scopes, a.Role, t.tierOf)
	} else {
		scopes, err = cleanScopes(req.Scopes, a.Role)
	}
	if err != nil {
		refuse(err.Error())
		return
	}
	tok, secret, err := t.auth.CreateToken(r.Context(), a.ID, req.Name, kind, scopes, t.now().Add(time.Duration(days)*24*time.Hour))
	var ve *auth.ValidationError
	switch {
	case errors.As(err, &ve):
		refuse(ve.Msg)
		return
	case err != nil:
		t.internal(w, "creating an API token failed", err)
		return
	}
	t.Audit.Change(r, "tokens.create", "token:"+tok.ID, map[string]any{"name": tok.Name, "kind": tok.Kind, "scopes": tok.Scopes, "expires_at": tok.ExpiresAt}, T1, nil)
	// The only response that ever carries the secret. /api/tokens is never
	// compressed (server.noCompress), so it can't leak through BREACH.
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"token": tok, "secret": secret})
}

// cleanScopes validates a requested scope list for a token of role: at
// least one, at most MaxTokenScopes, each one ValidTokenScope allows,
// without duplicates, in the order given.
func cleanScopes(in []string, role string) ([]string, error) {
	if len(in) == 0 {
		return nil, forbid("Pick at least one scope: what the token may read or run.")
	}
	if len(in) > MaxTokenScopes {
		return nil, forbid("A token can have at most %d scopes. Use a pattern such as proxmox.guest.* instead.", MaxTokenScopes)
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if err := ValidTokenScope(s, role); err != nil {
			return nil, err
		}
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out, nil
}

func (t *Tokens) revoke(w http.ResponseWriter, r *http.Request) {
	a, _ := ActorFrom(r)
	id := r.PathValue("id")
	tok, err := t.auth.GetToken(r.Context(), id)
	seen := err == nil
	if err == nil {
		if seen, err = t.visible(r.Context(), a, tok); err != nil {
			t.internal(w, "reading the owner failed", err)
			return
		}
	}
	if errors.Is(err, auth.ErrTokenNotFound) || (err == nil && !seen) {
		// Someone else's token you may not touch reads as missing, so IDs
		// don't leak; the attempt is audited.
		t.Audit.Refuse(r, "tokens.revoke", "token:"+clipString(id, 100), nil, T1, "not_found", "There's no such token.")
		httpx.Say(w, http.StatusNotFound, "not_found", msg.New("authz.token_not_found"))
		return
	} else if err != nil {
		t.internal(w, "reading an API token failed", err)
		return
	}
	tok, changed, err := t.auth.RevokeToken(r.Context(), id, a.ID)
	if err != nil {
		t.internal(w, "revoking an API token failed", err)
		return
	}
	if !changed {
		httpx.Say(w, http.StatusConflict, "already_revoked", msg.New("authz.token_already_revoked"))
		return
	}
	t.Audit.Change(r, "tokens.revoke", "token:"+tok.ID, map[string]string{"name": tok.Name, "user": tok.Username}, T1, nil)
	if t.OnRevoke != nil {
		t.OnRevoke(context.WithoutCancel(r.Context()), tok.ID)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]auth.APIToken{"token": tok})
}

// tierOf is a registered action's tier, for checking an agent's propose
// scopes when it names one action.
func (t *Tokens) tierOf(id string) (Tier, bool) {
	if t.Actions == nil {
		return "", false
	}
	for _, act := range t.Actions() {
		if act.ID == id {
			return act.Tier, true
		}
	}
	return "", false
}

// agentOffers is the picker's agent section: the reads an agent token may
// hold and the actions it may propose, each available or not with why.
type agentOffers struct {
	// On is the Agents switch: agent tokens can be made only while it's on.
	On       bool         `json:"on"`
	Reads    []scopeOffer `json:"reads"`
	Propose  []scopeOffer `json:"propose"`
	Families []scopeOffer `json:"families"`
	// Narrow names the qualifiers a read may carry: host, tag, lens.
	Narrow []string `json:"narrow"`
	Limits string   `json:"limits"`
}

// scopeOffer is one entry of the picker: available says whether the
// caller's role lets a token use it, and why not when it doesn't.
type scopeOffer struct {
	Scope       string `json:"scope"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Tier        Tier   `json:"tier,omitempty"`
	Available   bool   `json:"available"`
	Unavailable string `json:"unavailable,omitempty"`
}

func (t *Tokens) scopes(w http.ResponseWriter, r *http.Request) {
	a, _ := ActorFrom(r)
	reads := make([]scopeOffer, 0, len(ReadScopes))
	for _, rs := range ReadScopes {
		o := scopeOffer{Scope: rs.Scope, Label: rs.Label, Description: rs.Description, Available: rank(a.Role) >= rank(rs.MinRole)}
		if !o.Available {
			o.Unavailable = "Your role can't read this, so a token of yours can't either."
		}
		reads = append(reads, o)
	}
	actions, families := []scopeOffer{}, []scopeOffer{}
	seen := map[string]bool{}
	if t.Actions != nil {
		for _, act := range t.Actions() {
			o := scopeOffer{Scope: act.ID, Label: act.Title, Tier: act.Tier, Available: true}
			if o.Label == "" {
				o.Label = act.ID
			}
			switch {
			case act.Tier.rank() >= T3.rank():
				o.Available, o.Unavailable = false, "Tokens can't run T3 or T4 actions: they need a person to confirm in a session."
			case a.Role == auth.RoleHousehold && (!act.Household || act.Tier.rank() > HouseholdCap.rank()):
				o.Available, o.Unavailable = false, "Household tokens run only actions meant for the household."
			case a.Role != auth.RoleHousehold && act.Tier.rank() > tierCap(a.Role).rank():
				o.Available, o.Unavailable = false, msg.New("agents.offer_role", "role", a.Role, "tier", string(act.Tier)).Text()
			case act.ReadsData && !act.Household && rank(a.Role) < rank(auth.RoleOperator):
				o.Available, o.Unavailable = false, msg.New("agents.offer_reads_data").Text()
			}
			actions = append(actions, o)
			if fam, _, ok := strings.Cut(act.ID, "."); ok && !seen[fam] {
				seen[fam] = true
				families = append(families, scopeOffer{Scope: fam + ".*", Label: "Every " + fam + " action your role allows", Available: true})
			}
		}
	}
	agent := agentOffers{Reads: []scopeOffer{}, Propose: []scopeOffer{}, Families: []scopeOffer{}, Narrow: agentNarrowing,
		Limits: msg.New("agents.offer_limits").Text()}
	if t.AgentsOn != nil {
		if on, err := t.AgentsOn(r.Context()); err == nil {
			agent.On = on
		}
	}
	for _, rs := range AgentReadScopes {
		o := scopeOffer{Scope: rs.Scope, Label: rs.Label, Description: rs.Description, Available: rank(a.Role) >= rank(rs.MinRole)}
		if !o.Available {
			o.Unavailable = msg.New("agents.offer_read_role").Text()
		}
		agent.Reads = append(agent.Reads, o)
	}
	fams := map[string]bool{}
	if t.Actions != nil {
		for _, act := range t.Actions() {
			o := scopeOffer{Scope: ProposePrefix + act.ID, Label: act.Title, Tier: act.Tier, Available: true}
			if o.Label == "" {
				o.Label = act.ID
			}
			switch {
			case AgentForbiddenAction(act.ID):
				o.Available, o.Unavailable = false, msg.New("agents.offer_config").Text()
			case act.Tier.rank() >= T4.rank():
				o.Available, o.Unavailable = false, msg.New("agents.never_t4").Text()
			case a.Role == auth.RoleHousehold && (!act.Household || act.Tier.rank() > HouseholdCap.rank()):
				o.Available, o.Unavailable = false, msg.New("agents.offer_household").Text()
			case a.Role != auth.RoleHousehold && act.Tier.rank() > tierCap(a.Role).rank():
				o.Available, o.Unavailable = false, "Your role ("+a.Role+") can't run "+string(act.Tier)+" actions."
			case act.ReadsData && !act.Household && rank(a.Role) < rank(auth.RoleOperator):
				o.Available, o.Unavailable = false, "Only operators, admins and the owner can run this: what it reads back can hold secrets."
			case act.ReadsData:
				o.Description = msg.New("agents.offer_needs_logs").Text()
			}
			agent.Propose = append(agent.Propose, o)
			if fam, _, ok := strings.Cut(act.ID, "."); ok && !fams[fam] && !AgentForbiddenAction(fam+".") {
				fams[fam] = true
				agent.Families = append(agent.Families, scopeOffer{Scope: ProposePrefix + fam + ".*", Label: msg.New("agents.offer_family", "family", fam).Text(), Available: true})
			}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"agent": agent,
		"reads": reads, "families": families, "actions": actions,
		"all":          scopeOffer{Scope: "*", Label: "Everything your role allows", Description: "Every read scope your role may use and every action up to T2. T2 actions still wait for a person to approve them.", Available: true},
		"default_days": DefaultTokenDays, "max_days": MaxTokenDays,
		"approval": "Actions up to T1 run when the token approves its own plan. T2 actions wait in the Inbox until an admin or the owner approves them. T3 and T4 are never open to tokens.",
	})
}

// clipString bounds what a request named before it goes in the audit log.
func clipString(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// clipScopes bounds a requested scope list for the audit log.
func clipScopes(in []string) []string {
	out := make([]string, 0, min(len(in), MaxTokenScopes))
	for i, s := range in {
		if i == MaxTokenScopes {
			break
		}
		out = append(out, clipString(s, 200))
	}
	return out
}

func (t *Tokens) internal(w http.ResponseWriter, what string, err error) {
	t.log.Error("tokens: "+what, "err", err)
	httpx.Say(w, http.StatusInternalServerError, "internal", msg.New("hub.internal_error"))
}
