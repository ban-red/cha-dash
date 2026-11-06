package apidoc

import (
	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/displays"
)

// Mirrors of what internal/displays' handlers read and write.
type (
	displayList struct {
		Displays []displays.View `json:"displays"`
	}
	displayBody struct {
		Display displays.View `json:"display"`
	}
	displayCreated struct {
		Display displays.View `json:"display"`
		Secret  string        `json:"secret"`
	}
	displayUpdate struct {
		Name  *string     `json:"name,omitempty"`
		Style *auth.Style `json:"style,omitempty"` // wall, tv or ink
		Quiet *auth.Quiet `json:"quiet,omitempty"` // null clears the quiet hours
	}
	displayClaim struct {
		Token string `json:"token"`
	}
)

func init() {
	add("displays", map[string]Op{
		"GET /api/displays":            {Summary: "The displays not revoked, newest first.", Resp: displayList{}},
		"POST /api/displays":           {Summary: "Make a display for one board. The response carries its secret, once.", Body: displays.CreateRequest{}, Status: 201, Resp: displayCreated{}},
		"PATCH /api/displays/{id}":     {Summary: "Rename a display, or set or clear its quiet hours.", Body: displayUpdate{}, Resp: displayBody{}, Errors: []string{"not_found"}},
		"POST /api/displays/{id}/link": {Summary: "A new link for a display: its claim code, once, good for a day. The device on the old link keeps working until the new one is claimed.", Resp: displayCreated{}, Errors: []string{"not_found"}},
		"DELETE /api/displays/{id}":    {Summary: "Revoke a display at once; its sockets close.", Errors: []string{"not_found"}},
		"POST /api/display/claim":      {Summary: "A device presents its display's claim code (from the link's fragment), once, and gets the display's device secret in a cookie scoped to /api/display.", Body: displayClaim{}, Errors: []string{"invalid_display", "rate_limited"}},
		"GET /api/display":             {Summary: "The display's board, its layout and icons, and the hub's time zone.", Resp: displays.Read{}, Errors: []string{"invalid_display"}},
		"GET /api/display/ws":          {Summary: "The display's realtime WebSocket: the graph, metrics, causes, inbox and backups, narrowed to its board.", Upgrade: true, Errors: []string{"invalid_display"}},
		"GET /api/display/icons/{id}":  {Summary: "One cached icon, for a display's tiles.", Media: "image/*", Query: []Param{{Name: "v", Desc: "The icon's version, for long caching."}}},
	})
}
