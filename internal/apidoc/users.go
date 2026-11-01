package apidoc

import (
	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/authz"
)

// Mirrors of what internal/authz's users, bans and tokens handlers read
// and write.
type (
	newUser struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	userUpdate struct {
		Role     *string `json:"role"`
		Disabled *bool   `json:"disabled"`
	}
	tokenList struct {
		Tokens []auth.APIToken `json:"tokens"`
	}
	tokenBody struct {
		Token auth.APIToken `json:"token"`
	}
	newToken struct {
		Name          string   `json:"name"`
		Kind          string   `json:"kind,omitempty"` // "api" (the default) or "agent", which needs Agents on
		Scopes        []string `json:"scopes"`
		ExpiresInDays *int     `json:"expires_in_days"`
	}
	tokenCreated struct {
		Token  auth.APIToken `json:"token"`
		Secret string        `json:"secret"`
	}
	scopeOffer struct {
		Scope       string     `json:"scope"`
		Label       string     `json:"label"`
		Description string     `json:"description,omitempty"`
		Tier        authz.Tier `json:"tier,omitempty"`
		Available   bool       `json:"available"`
		Unavailable string     `json:"unavailable,omitempty"`
	}
	scopePicker struct {
		Reads       []scopeOffer `json:"reads"`
		Families    []scopeOffer `json:"families"`
		Actions     []scopeOffer `json:"actions"`
		All         scopeOffer   `json:"all"`
		DefaultDays int          `json:"default_days"`
		MaxDays     int          `json:"max_days"`
		Approval    string       `json:"approval"`
	}
)

func init() {
	add("users", map[string]Op{
		"GET /api/users":                          {Summary: "Every user and their role.", Resp: []auth.UserInfo{}},
		"POST /api/users":                         {Summary: "Add a user with a role.", Body: newUser{}, Status: 201, Resp: auth.UserInfo{}},
		"PUT /api/users/{id}":                     {Summary: "Change a user's role, or disable them.", Body: userUpdate{}, Resp: auth.UserInfo{}},
		"POST /api/users/{id}/transfer-ownership": {Summary: "Hand ownership to another user. Needs a step-up.", Resp: auth.UserInfo{}, Errors: []string{"step_up_required"}},
		"GET /api/users/{id}/identities":          {Summary: "Someone's linked sign-ins, read only.", Resp: identitiesView{}},
		"GET /api/bans":                           {Summary: "Addresses banned after too many wrong passwords.", Resp: []auth.Ban{}},
		"DELETE /api/bans/{ip}":                   {Summary: "Lift a ban."},
	})
	add("tokens", map[string]Op{
		"GET /api/tokens":         {Summary: "API tokens: your own; admins and the owner see everyone's.", Resp: tokenList{}},
		"POST /api/tokens":        {Summary: "Make an API token. The secret is in this answer only.", Body: newToken{}, Status: 201, Resp: tokenCreated{}, Errors: []string{"invalid", "agents_off"}},
		"DELETE /api/tokens/{id}": {Summary: "Revoke an API token.", Resp: tokenBody{}, Errors: []string{"already_revoked"}},
		"GET /api/tokens/scopes":  {Summary: "The scopes your role may give a token.", Resp: scopePicker{}},
	})
}
