package boards

import (
	"context"
	"encoding/json"
	"strings"
)

// Scope is what the boards show, read from their latest layouts and tiles:
// the resources they name, and the kinds of resource the widgets without a
// named target show (a fleet tile shows every host). The inbox uses it to
// decide which incidents the household role sees (topics.InboxView).
type Scope struct {
	// All: some board shows everything, or something this can't narrow
	// down (a board never saved, which gets the default layout; the
	// Incidents widget; a widget type it doesn't know).
	All bool
	// IDs are resources a board names: an item's target (<type>@<id>), a
	// tile's bind.resource, an integration a tile binds (integration:<id>).
	IDs map[string]bool
	// Kinds are resource kinds a target-less widget shows.
	Kinds map[string]bool
}

// Shows reports whether a resource with id and kind is on a board.
func (s Scope) Shows(id, kind string) bool {
	return s.All || s.IDs[id] || (kind != "" && s.Kinds[kind])
}

// widgetKinds are the resource kinds a widget shows when its item names no
// target: its "the first one" or "all of them". Widgets with nothing behind
// them (the clock, bookmarks, weather) show no resource's incidents. A type
// missing here, and the Incidents widget, make the scope All.
var widgetKinds = map[string][]string{
	"clock":         nil,
	"bookmarks":     nil,
	"weather":       {"weather"},
	"fleet":         {"host", "cluster", "vm", "lxc"},
	"host":          {"host"},
	"guests":        {"vm", "lxc"},
	"containers":    {"container", "engine"},
	"unraid":        {"array", "disk", "pool", "host"},
	"storage":       {"storage", "pool", "array", "datastore", "disk"},
	"backups":       {"datastore", "backup_job"},
	"opnsense":      {"router", "interface"},
	"cameras":       {"camera"},
	"check":         {"check"},
	"homeassistant": {"app"},
	"calendar":      {"app"},
	"plex":          {"app"},
	"downloads":     {"app"},
	"arr":           {"app"},
	"traefik":       {"app"},
	"uptimekuma":    {"app", "check"},
	"service":       {"app"},
}

// Scope reads what every board shows.
func (s *Service) Scope(ctx context.Context) (Scope, error) {
	all, err := ReadAll(ctx, s.st.Reader())
	if err != nil {
		return Scope{}, err
	}
	sc := Scope{IDs: map[string]bool{}, Kinds: map[string]bool{}}
	if len(all) == 0 {
		sc.All = true // the first GET /api/boards creates Home, with the default layout
	}
	for _, d := range all {
		addBoard(&sc, d.Layout)
	}
	return sc, nil
}

// layoutDoc is the part of a layout document Scope reads: its items.
type layoutDoc struct {
	Sections []struct {
		Items []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"items"`
	} `json:"sections"`
}

func addBoard(sc *Scope, l Layout) {
	var doc layoutDoc
	if len(l.Document) == 0 || string(l.Document) == "null" || json.Unmarshal(l.Document, &doc) != nil {
		sc.All = true // never saved, or unreadable: the page lays it out from the whole graph
		return
	}
	tiles, _ := ParseTiles(l.Tiles)
	for _, sec := range doc.Sections {
		for _, it := range sec.Items {
			named := false
			if t, ok := tiles[it.ID]; ok && t.Bind != nil {
				for _, id := range []string{t.Bind.Resource, instanceResource(t.Bind.Instance)} {
					if id != "" {
						sc.IDs[id], named = true, true
					}
				}
				for _, src := range t.Bind.Sources {
					if id := instanceResource(src.Instance); id != "" {
						sc.IDs[id], named = true, true
					}
				}
				if t.Bind.Check != "" {
					named = true // a check by name: its resource isn't known here
				}
			}
			if _, target, ok := strings.Cut(it.ID, "@"); ok && target != "" {
				sc.IDs[target], named = true, true
			}
			if named {
				continue
			}
			kinds, known := widgetKinds[it.Type]
			if !known {
				sc.All = true
				continue
			}
			for _, k := range kinds {
				sc.Kinds[k] = true
			}
		}
	}
}

func instanceResource(id string) string {
	if id == "" {
		return ""
	}
	return "integration:" + id
}
