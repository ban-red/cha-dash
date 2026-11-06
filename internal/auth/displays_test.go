package auth

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A display's life in the store: made for a board, its secret stored only
// as a hash and never logged; it authenticates as the display alone (no
// user, no session, no token); its last sighting is throttled; renaming
// and quiet hours validate; revoking is final, says so only to the right
// secret, and emits EventDisplayRevoked.
func TestDisplayLifecycle(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	u, err := h.svc.Setup(ctx, "sam", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.Writer().Exec(`INSERT INTO boards (id, slug, title, position, created_at, updated_at) VALUES ('brd_1', 'home', 'Home', 0, 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.svc.CreateDisplay(ctx, u.ID, "Hall", "brd_nope", "", nil); !errors.Is(err, ErrNoSuchBoard) {
		t.Fatalf("an unknown board: %v", err)
	}
	for _, q := range []*Quiet{{"22:00", "22:00"}, {"7:00", "22:00"}, {"22:00", "24:00"}, {"", "07:00"}} {
		var ve *ValidationError
		if _, _, err := h.svc.CreateDisplay(ctx, u.ID, "Hall", "brd_1", "", q); !errors.As(err, &ve) {
			t.Errorf("quiet %+v: %v", q, err)
		}
	}
	var revokedEvents []string
	h.svc.OnEvent(func(e Event) {
		if e.Kind == EventDisplayRevoked {
			revokedEvents = append(revokedEvents, e.Subject)
		}
	})

	d, secret, err := h.svc.CreateDisplay(ctx, u.ID, "  Hall tablet ", "brd_1", "", &Quiet{"22:00", "07:00"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "Hall tablet" || d.BoardID != "brd_1" || d.BoardName != "Home" || d.CreatedBy != "sam" || d.Quiet == nil || d.Style != StyleWall ||
		!strings.HasPrefix(d.ID, "dsp_") || !displaySecretRE.MatchString(secret) || !strings.Contains(secret, strings.TrimPrefix(d.ID, "dsp_")) {
		t.Fatalf("created %+v %q", d, secret)
	}
	var stored []byte
	if err := h.st.Reader().QueryRow(`SELECT token_hash FROM displays WHERE id = ?`, d.ID).Scan(&stored); err != nil || bytes.Contains(stored, []byte(secret)) || len(stored) != 32 {
		t.Fatalf("stored %x %v", stored, err)
	}

	id, err := h.svc.AuthenticateDisplay(ctx, secret, "192.0.2.5")
	if err != nil || id.Display == nil || id.Display.ID != d.ID || id.Display.BoardID != "brd_1" || id.User.ID != "" || id.Session.ID != "" || id.Token != nil {
		t.Fatalf("authenticate: %+v %v", id, err)
	}
	got, _ := h.svc.GetDisplay(ctx, d.ID)
	if got.LastSeenAt == nil || got.LastSeenIP != "192.0.2.5" {
		t.Fatalf("last seen: %+v", got)
	}
	seen := *got.LastSeenAt
	h.clock.advance(30 * time.Second)
	_, _ = h.svc.AuthenticateDisplay(ctx, secret, "192.0.2.6")
	if got, _ := h.svc.GetDisplay(ctx, d.ID); !got.LastSeenAt.Equal(seen) {
		t.Fatal("last seen written twice within a minute")
	}
	// A wrong secret for the right ID, a malformed one, an API token.
	wrong := secret[:len(secret)-1] + map[bool]string{true: "B", false: "A"}[strings.HasSuffix(secret, "A")]
	for _, bad := range []string{wrong, "cdd_x", "cdt_" + strings.Repeat("a", 26) + "_" + strings.Repeat("A", 52)} {
		if _, err := h.svc.AuthenticateDisplay(ctx, bad, ""); !errors.Is(err, ErrBadDisplay) {
			t.Errorf("%q: %v", bad, err)
		}
	}

	name, tv, bad := "Kitchen", StyleTV, Style("hologram")
	if _, err := h.svc.UpdateDisplay(ctx, d.ID, nil, &bad, false, nil); err == nil {
		t.Fatal("an unknown style was taken")
	}
	if _, _, err := h.svc.CreateDisplay(ctx, u.ID, "Hall", "brd_1", bad, nil); err == nil {
		t.Fatal("an unknown style was taken at creation")
	}
	if d, err = h.svc.UpdateDisplay(ctx, d.ID, &name, &tv, true, nil); err != nil || d.Name != "Kitchen" || d.Quiet != nil || d.Style != StyleTV {
		t.Fatalf("update: %+v %v", d, err)
	}
	if list, _ := h.svc.ListDisplays(ctx); len(list) != 1 {
		t.Fatalf("list: %+v", list)
	}

	if _, changed, err := h.svc.RevokeDisplay(ctx, d.ID, u.ID); err != nil || !changed {
		t.Fatalf("revoke: %v %v", changed, err)
	}
	if _, changed, _ := h.svc.RevokeDisplay(ctx, d.ID, u.ID); changed {
		t.Fatal("revoked twice")
	}
	if len(revokedEvents) != 1 || revokedEvents[0] != d.ID {
		t.Fatalf("events: %v", revokedEvents)
	}
	if _, err := h.svc.AuthenticateDisplay(ctx, secret, ""); !errors.Is(err, ErrDisplayRevoked) {
		t.Fatalf("revoked: %v", err)
	}
	if _, err := h.svc.AuthenticateDisplay(ctx, wrong, ""); !errors.Is(err, ErrBadDisplay) {
		t.Fatalf("a wrong secret learns nothing about the revocation: %v", err)
	}
	if ok, _ := h.svc.DisplayStillValid(ctx, id); ok {
		t.Fatal("a revoked display is still valid")
	}
	if _, err := h.svc.UpdateDisplay(ctx, d.ID, &name, nil, false, nil); !errors.Is(err, ErrDisplayNotFound) {
		t.Fatalf("updating a revoked display: %v", err)
	}
	if list, _ := h.svc.ListDisplays(ctx); len(list) != 0 {
		t.Fatalf("a revoked display is listed: %+v", list)
	}
	if strings.Contains(h.logs.String(), secret) {
		t.Fatal("the secret reached the log")
	}
}

// RequireDisplay takes a display's cookie or Bearer cdd_, and nothing
// else: no session cookie, no API token.
func TestRequireDisplay(t *testing.T) {
	h, u, _ := tokenHarness(t)
	ctx := t.Context()
	if _, err := h.st.Writer().Exec(`INSERT INTO boards (id, slug, title, position, created_at, updated_at) VALUES ('brd_1', 'home', 'Home', 0, 1, 1)`); err != nil {
		t.Fatal(err)
	}
	_, secret, err := h.svc.CreateDisplay(ctx, u.ID, "Hall", "brd_1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, tokSecret, err := h.svc.CreateToken(ctx, u.ID, "script", KindAPI, []string{"*"}, h.clock.now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h.svc.RequireDisplay(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := FromContext(r.Context())
		_, _ = w.Write([]byte(id.Display.ID))
	})))
	t.Cleanup(srv.Close)
	call := func(hdr map[string]string) int {
		req, _ := http.NewRequest("GET", srv.URL, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for name, c := range map[string]struct {
		hdr  map[string]string
		want int
	}{
		"bearer":          {map[string]string{"Authorization": "Bearer " + secret}, 200},
		"cookie":          {map[string]string{"Cookie": DisplayCookie + "=" + secret}, 200},
		"nothing":         {nil, 401},
		"API token":       {map[string]string{"Authorization": "Bearer " + tokSecret}, 401},
		"session cookie":  {map[string]string{"Cookie": CookieName + "=" + secret}, 401},
		"token as cookie": {map[string]string{"Cookie": DisplayCookie + "=" + tokSecret}, 401},
	} {
		if got := call(c.hdr); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
}
