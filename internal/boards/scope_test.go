package boards

import (
	"encoding/json"
	"testing"
)

func TestScope(t *testing.T) {
	s, _ := newService(t)
	ctx := t.Context()

	// No board yet: the first one gets the default layout, which shows all.
	if sc, err := s.Scope(ctx); err != nil || !sc.All {
		t.Fatalf("no boards: %+v, %v", sc, err)
	}
	list, err := s.List(ctx) // creates Home, never saved
	if err != nil {
		t.Fatal(err)
	}
	if sc, _ := s.Scope(ctx); !sc.All {
		t.Fatalf("unsaved board: %+v", sc)
	}

	layout := `{"version":1,"columns":12,"sections":[{"id":"home","items":[
		{"id":"host@unraid:jupiter","type":"host"},
		{"id":"fleet","type":"fleet"},
		{"id":"clock","type":"clock"},
		{"id":"service@tile:x1","type":"service"},
		{"id":"arr@app:sonarr","type":"arr"},
		{"id":"queue","type":"calendar"}]}]}`
	tiles := `{"service@tile:x1":{"name":"Photos","bind":{"resource":"docker:i_d/container/immich"}},
		"queue":{"bind":{"sources":[{"instance":"i_s","capability":"media.calendar"}]}},
		"arr@app:sonarr":{"bind":{"instance":"i_x"}}}`
	if _, err := s.SaveLayout(ctx, list[0].ID, 0, 1, json.RawMessage(layout), json.RawMessage(tiles), ""); err != nil {
		t.Fatal(err)
	}
	sc, err := s.Scope(ctx)
	if err != nil || sc.All {
		t.Fatalf("saved board: %+v, %v", sc, err)
	}
	for _, c := range []struct {
		id, kind string
		want     bool
	}{
		{"unraid:jupiter", "host", true},                   // named
		{"docker:i_d/container/immich", "container", true}, // a tile's bind.resource
		{"integration:i_x", "integration", true},           // a tile's bind.instance
		{"integration:i_s", "integration", true},           // a calendar's source
		{"app:sonarr", "app", true},
		{"proxmox:i_p/node/io", "host", true}, // the fleet shows hosts
		{"proxmox:i_p/qemu/201", "vm", true},  // and their guests
		{"docker:i_d/container/x", "container", false},
		{"app:plex", "app", false}, // the calendar names its sources, so no kind
	} {
		if got := sc.Shows(c.id, c.kind); got != c.want {
			t.Errorf("Shows(%s, %s) = %v, want %v", c.id, c.kind, got, c.want)
		}
	}

	// The Incidents widget, or a type nobody knows, shows everything.
	for _, typ := range []string{"incidents", "future-widget"} {
		doc := `{"sections":[{"items":[{"id":"x","type":"` + typ + `"}]}]}`
		l, _ := s.Layout(ctx, list[0].ID)
		if _, err := s.SaveLayout(ctx, list[0].ID, l.Revision, 1, json.RawMessage(doc), nil, ""); err != nil {
			t.Fatal(err)
		}
		if sc, _ := s.Scope(ctx); !sc.All {
			t.Errorf("%s: %+v", typ, sc)
		}
	}
}
