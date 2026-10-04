package homeassistant

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"dash.cha.sh/internal/integration"
)

// wsTimeout bounds Test's WebSocket exchange.
const wsTimeout = 10 * time.Second

// user is the WebSocket API's auth/current_user result.
type user struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	IsOwner bool   `json:"is_owner"`
	IsAdmin bool   `json:"is_admin"`
}

// Test implements integration.Instance: the REST API answers with the
// token, the version, and whose rights the token carries.
func (in *instance) Test(ctx context.Context) integration.TestResult {
	var api struct {
		Message string `json:"message"`
	}
	if err := in.getJSON(ctx, "/api/", nil, &api); err != nil {
		return integration.Failed(err)
	}
	if api.Message == "" {
		return integration.Failed(&integration.RepairError{Code: integration.CodeAPIChanged, Message: "GET /api/ didn't say the API is running: this doesn't look like Home Assistant"})
	}
	var cfg haConfig
	if err := in.getJSON(ctx, "/api/config", nil, &cfg); err != nil {
		return integration.Failed(err)
	}
	res := integration.TestResult{OK: true, Version: cfg.Version}
	u, err := in.currentUser(ctx)
	switch {
	case err != nil:
		res.Grants = []integration.Grant{{Name: "Home Assistant user", Access: integration.AccessReadWrite,
			Detail: "A long-lived token carries all of its user's rights: at least reading every entity and calling services, so it can control devices. Cha Dash couldn't tell whether the user is an administrator."}}
		code, _, msg := integration.Classify(err)
		res.Warnings = append(res.Warnings, integration.Warning{Code: "ha_user_unknown", Severity: integration.WarnInfo,
			Message: fmt.Sprintf("Cha Dash couldn't read which user this token belongs to over the WebSocket API (%s: %s).", code, msg)})
	case u.IsAdmin:
		who := "an administrator"
		if u.IsOwner {
			who = "the owner"
		}
		res.Grants = []integration.Grant{{Name: "Home Assistant administrator", Access: integration.AccessAdmin,
			Detail: fmt.Sprintf("This long-lived token carries all the rights of %s (%s): it can control every device, change the configuration and manage users. A non-admin user's token is safer.", u.Name, who)}}
	default:
		res.Grants = []integration.Grant{{Name: "Home Assistant user", Access: integration.AccessReadWrite,
			Detail: fmt.Sprintf("This long-lived token carries all the rights of %s, who isn't an administrator: it can read every entity and call services, so it can still control devices. Home Assistant has no read-only tokens.", u.Name)}}
	}
	return res
}

// currentUser asks the WebSocket API who the token belongs to: wait for
// auth_required, authenticate with the token (in the message, never the
// URL), send auth/current_user, read the result, close.
func (in *instance) currentUser(ctx context.Context) (user, error) {
	ctx, cancel := context.WithTimeout(ctx, wsTimeout)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, in.base.JoinPath("api", "websocket").String(), &websocket.DialOptions{HTTPClient: in.http})
	if err != nil {
		return user{}, err
	}
	defer conn.CloseNow()
	var msg struct {
		ID      int    `json:"id"`
		Type    string `json:"type"`
		Message string `json:"message"`
		Success bool   `json:"success"`
		Result  user   `json:"result"`
		Error   struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := wsjson.Read(ctx, conn, &msg); err != nil {
		return user{}, err
	}
	if msg.Type != "auth_required" {
		return user{}, fmt.Errorf("the WebSocket API opened with %q, not auth_required", msg.Type)
	}
	if err := wsjson.Write(ctx, conn, map[string]string{"type": "auth", "access_token": in.token}); err != nil {
		return user{}, err
	}
	if err := wsjson.Read(ctx, conn, &msg); err != nil {
		return user{}, err
	}
	switch msg.Type {
	case "auth_ok":
	case "auth_invalid":
		return user{}, &integration.AuthError{Field: "token", Message: "the WebSocket API rejected the token"}
	default:
		return user{}, fmt.Errorf("the WebSocket API answered the token with %q", msg.Type)
	}
	if err := wsjson.Write(ctx, conn, map[string]any{"id": 1, "type": "auth/current_user"}); err != nil {
		return user{}, err
	}
	for {
		msg.ID, msg.Type = 0, ""
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			return user{}, err
		}
		if msg.ID != 1 || msg.Type != "result" {
			continue
		}
		if !msg.Success {
			return user{}, errors.New("auth/current_user failed: " + msg.Error.Message)
		}
		conn.Close(websocket.StatusNormalClosure, "")
		return msg.Result, nil
	}
}
