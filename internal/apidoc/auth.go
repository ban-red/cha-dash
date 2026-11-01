package apidoc

import (
	"encoding/json"
	"time"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/httpx"
)

// Mirrors of what internal/auth's and internal/authz's handlers write.
type (
	setupStatus struct {
		Required bool              `json:"required"`
		SignIn   auth.PublicSignIn `json:"sign_in"`
		DevLogin bool              `json:"dev_login,omitempty"`
	}
	credentials struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	userBody struct {
		User auth.User `json:"user"`
	}
	sessionBody struct {
		User          auth.User    `json:"user"`
		Session       auth.Session `json:"session"`
		StepUpUntil   *time.Time   `json:"step_up_until"`
		StepUpMethods []string     `json:"step_up_methods"`
	}
	// WebAuthn options and credentials pass through as the browser's
	// PublicKeyCredential JSON: their shape is the WebAuthn spec's.
	publicKeyOptions struct {
		PublicKey json.RawMessage `json:"publicKey"`
	}
	passkeyCreated struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	unknownPasskey struct {
		httpx.ErrorBody
		Signal struct {
			RPID         string `json:"rpId"`
			CredentialID string `json:"credentialId"`
		} `json:"signal"`
	}
	nameBody struct {
		Name string `json:"name"`
	}
	stepUpBegin struct {
		Method    string          `json:"method"`
		PublicKey json.RawMessage `json:"publicKey,omitempty"`
		URL       string          `json:"url,omitempty"`
		Provider  string          `json:"provider,omitempty"`
	}
	stepUpGranted struct {
		Until time.Time `json:"until"`
	}
	identitiesView struct {
		Identities                 []auth.LinkedIdentity `json:"identities"`
		Provider                   string                `json:"provider,omitempty"`
		ForwardAuthUsername        string                `json:"forward_auth_username,omitempty"`
		ForwardAuthLinkedElsewhere bool                  `json:"forward_auth_linked_elsewhere,omitempty"`
	}
	usernameBody struct {
		Username string `json:"username"`
	}
	versionBody struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
	}
)

func init() {
	add("auth", map[string]Op{
		"GET /api/setup": {Summary: "Whether first-run setup is still needed, and how sign-in looks.", Resp: setupStatus{}},
		"POST /api/setup": {Summary: "Create the owner on first run and sign in.", Body: credentials{}, Status: 201, Resp: userBody{},
			Errors: []string{"setup_complete", "invalid", "unknown_host"}},
		"POST /api/auth/login":                {Summary: "Sign in with a username and password.", Body: credentials{}, Resp: userBody{}, Errors: []string{"bad_credentials", "rate_limited", "ip_banned"}},
		"POST /api/auth/logout":               {Summary: "Sign out: end the session and clear its cookie."},
		"POST /api/auth/passkeys/login/begin": {Summary: "Start a usernameless passkey sign-in: the request options.", Resp: publicKeyOptions{}},
		"POST /api/auth/passkeys/login/finish": {Summary: "Finish a passkey sign-in with the assertion's toJSON().", Body: json.RawMessage{}, Resp: userBody{}, Fail: map[int]any{401: unknownPasskey{}},
			Errors: []string{"ceremony_expired", "passkey_rejected", "unknown_passkey"}},
		"POST /api/auth/dev-login":                {Summary: "Sign in as the owner in one click: -tags devlogin builds, from this machine only.", Resp: userBody{}},
		"GET /api/auth/oidc/start":                {Summary: "Go to the OIDC provider to sign in, link or step up.", Redirect: true, Query: []Param{{Name: "return", Desc: "The path to come back to."}, {Name: "link", Desc: "1 links the provider account to you."}, {Name: "step_up", Desc: "1 steps up at the provider."}}},
		"GET /api/auth/oidc/callback":             {Summary: "The OIDC provider's redirect back to the hub.", Redirect: true, Query: []Param{{Name: "code", Desc: "The authorization code."}, {Name: "state", Desc: "The flow's state."}}},
		"GET /api/auth/session":                   {Summary: "The signed-in user, the session and its step-up state.", Resp: sessionBody{}},
		"GET /api/version":                        {Summary: "The hub's version and commit.", Resp: versionBody{}},
		"POST /api/auth/passkeys/register/begin":  {Summary: "Start adding a passkey: the creation options.", Resp: publicKeyOptions{}},
		"POST /api/auth/passkeys/register/finish": {Summary: "Finish adding a passkey with the credential's toJSON().", Body: json.RawMessage{}, Status: 201, Resp: passkeyCreated{}, Errors: []string{"ceremony_expired", "passkey_rejected", "passkey_exists"}},
		"GET /api/auth/passkeys":                  {Summary: "Your passkeys, oldest first.", Resp: []auth.Passkey{}},
		"PATCH /api/auth/passkeys/{id}":           {Summary: "Rename one of your passkeys.", Body: nameBody{}, Resp: auth.Passkey{}},
		"DELETE /api/auth/passkeys/{id}":          {Summary: "Delete one of your passkeys.", Errors: []string{"last_sign_in_method"}},
		"POST /api/auth/step-up/begin":            {Summary: "Start a step-up: how to confirm it's you.", Resp: stepUpBegin{}},
		"POST /api/auth/step-up": {Summary: "Confirm it's you with a passkey assertion or {\"password\"}, for a few minutes.", Body: json.RawMessage{}, Resp: stepUpGranted{},
			Errors: []string{"step_up_failed", "passkey_required", "ceremony_expired"}},
		"GET /api/auth/identities":                       {Summary: "Your linked sign-ins (OIDC, forward-auth).", Resp: identitiesView{}},
		"DELETE /api/auth/identities/{issuer}/{subject}": {Summary: "Unlink one of your sign-ins. Needs a step-up.", Errors: []string{"step_up_required", "last_sign_in_method"}},
		"POST /api/auth/identities/forward-auth":         {Summary: "Link the username your trusted proxy sends to you. Needs a step-up.", Status: 201, Resp: usernameBody{}, Errors: []string{"no_forward_auth", "already_linked"}},
	})
}
