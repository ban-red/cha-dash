package act

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"dash.cha.sh/internal/action"
)

func TestApp(t *testing.T) {
	resolve := App("pihole:i_a/app", "Pi-hole")
	tg, err := resolve(t.Context(), "pihole:i_a/app")
	if err != nil || tg.ID != "pihole:i_a/app" || tg.Kind != "app" || tg.Name != "Pi-hole" {
		t.Fatalf("%+v, %v", tg, err)
	}
	for _, other := range []string{"pihole:i_b/app", "pihole:i_a/app/x", ""} {
		if _, err := resolve(t.Context(), other); !errors.Is(err, action.ErrNotFound) {
			t.Errorf("%q: %v", other, err)
		}
	}
}

func TestUndoAndSteps(t *testing.T) {
	u := UndoBy("unraid.container.stop", "Stop")(action.Request{Target: action.Target{Name: "plex"}})
	if u.Action != "unraid.container.stop" || u.Label != "Stop plex" || !json.Valid(u.Params) || string(u.Params) != "{}" {
		t.Fatalf("undo = %+v", u)
	}
	if s := Steps("a", "b"); len(s) != 2 || s[1].Label != "b" {
		t.Fatalf("steps = %+v", s)
	}
	if Fingerprint("container", "plex", "RUNNING") == Fingerprint("container", "plex", "EXITED") ||
		Fingerprint("a", "bc") == Fingerprint("ab", "c") {
		t.Fatal("fingerprints collide")
	}
}

func TestUntil(t *testing.T) {
	every := Every
	Every = time.Millisecond
	t.Cleanup(func() { Every = every })

	n := 0
	err := Until(t.Context(), nil, "", func(context.Context) (bool, error) {
		n++
		if n < 3 {
			return false, errors.New("not yet")
		}
		return true, nil
	})
	if err != nil || n != 3 {
		t.Fatalf("%v after %d", err, n)
	}

	// An *action.Error ends it at once.
	n = 0
	err = Until(t.Context(), nil, "", func(context.Context) (bool, error) {
		n++
		return false, action.Fail("search_failed", "it failed")
	})
	var ae *action.Error
	if !errors.As(err, &ae) || n != 1 {
		t.Fatalf("%v after %d", err, n)
	}

	// Out of time: the last reason, not the deadline.
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	err = Until(ctx, nil, "", func(ctx context.Context) (bool, error) {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, errors.New("plex reports exited, not running")
	})
	if err == nil || err.Error() != "plex reports exited, not running" {
		t.Fatalf("timeout = %v", err)
	}
	ctx2, cancel2 := context.WithTimeout(t.Context(), 5*time.Millisecond)
	defer cancel2()
	if err := Until(ctx2, nil, "", func(context.Context) (bool, error) { return false, nil }); err == nil || err.Error() != "the change didn't show in time" {
		t.Fatalf("silent timeout = %v", err)
	}
}
