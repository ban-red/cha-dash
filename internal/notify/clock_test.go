package notify

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/clock"
	"dash.cha.sh/internal/testfakes"
)

// The morning digest on a running service: nothing before its time; when
// the hub's clock reaches it, the service's own loop (Run) sends it, to an
// ntfy server and to an admin's phone, where it decrypts to the text the
// phone shows. 14b's manual test 11, on the test clock and the fakes
// (docs/plan/17-test-automation.md, H2 and H3).
func TestDigestArrivesAtItsTimeOnTheHubClock(t *testing.T) {
	e := newEnv(t)
	hub := clock.NewTest()
	s, err := New(t.Context(), Options{Store: e.st, Vault: e.v, Inbox: e.inbox, PublicURL: "https://dash.lab.example", Version: "test",
		Log: slog.New(slog.DiscardHandler), Clock: hub, Location: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	s.allowLoopbackPush = true
	push := testfakes.NewPush(t)
	s.pushClient = push.Client()

	ntfy := testfakes.NewCapture(t)
	if _, err := s.CreateChannel(t.Context(), ChannelInput{Kind: ChannelNtfy, Name: new("Phones"),
		Config: json.RawMessage(`{"server":"` + ntfy.URL + `","topic":"lab-alerts"}`)}); err != nil {
		t.Fatal(err)
	}
	phone, endpoint := push.Register("/push/owner-phone")
	var in SubscribeInput
	in.Endpoint, in.Keys.P256dh, in.Keys.Auth, in.Label = endpoint, phone.P256dh(), phone.Auth(), "Phone"
	if _, err := s.Subscribe(t.Context(), "u_owner", in); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateRule(t.Context(), RuleDigest, RuleUpdate{Enabled: new(true)}, "u_owner"); err != nil {
		t.Fatal(err)
	}
	// The digest an hour from now on the hub's clock.
	at := hub.Now().UTC().Add(time.Hour).Format("15:04")
	if err := s.saveDigest(t.Context(), storedDigest{Time: at}); err != nil {
		t.Fatal(err)
	}
	s.SetDigestSources(DigestSources{
		Overnight: func(context.Context, time.Time, time.Time) ([]DigestIncident, error) {
			return []DigestIncident{{ID: "i_1", Title: "Plex is down", Severity: "down", Resolved: true}}, nil
		},
		Updates: func(context.Context) (int, error) { return 2, nil },
	})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go s.Run(ctx)

	time.Sleep(300 * time.Millisecond)
	if n := len(ntfy.Requests()) + len(push.Messages()); n != 0 {
		t.Fatalf("%d messages before the digest's time", n)
	}
	if err := hub.Advance(61 * time.Minute); err != nil {
		t.Fatal(err)
	}
	// Past its time, and past the group window that holds messages back.
	time.Sleep(300 * time.Millisecond)
	if err := hub.Advance(DefaultGroupWindow + time.Second); err != nil {
		t.Fatal(err)
	}

	var sent struct{ Title, Message string }
	if err := ntfy.Wait(t, 1, 5*time.Second)[0].JSON(&sent); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sent.Message, "Plex is down") {
		t.Errorf("ntfy got %+v", sent)
	}
	var shown pushMessage
	got := push.Wait(t, 1, 5*time.Second)[0]
	if err := got.JSON(&shown); err != nil {
		t.Fatal(err)
	}
	if !got.VAPIDOK || got.Encoding != "aes128gcm" || shown.Kind != KindDigest || !strings.Contains(shown.Title+shown.Body, "Plex is down") {
		t.Errorf("the phone shows %+v (vapid %v, encoding %q)", shown, got.VAPIDOK, got.Encoding)
	}

	// Once a day: another move inside the late window sends nothing more.
	if err := hub.Advance(30 * time.Minute); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if n := len(ntfy.Requests()); n != 1 {
		t.Errorf("%d digests, want one", n)
	}
}
