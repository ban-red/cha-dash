package boards

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"dash.cha.sh/internal/msg"
)

// Tiles is a board's tile configuration: what each layout item shows that the
// layout document can't carry, keyed by layout item id. It's stored with
// every layout revision (layouts.tiles), so a revision is a layout and its
// tiles together. Unlike the layout document, the hub owns this shape.
//
//	{
//	  "arr@sonarr:i_x/app": {
//	    "name": "TV", "icon": "sonarr.png", "href": "https://sonarr.lab.example",
//	    "bind": {"instance": "i_x"}, "options": {"queue": true},
//	    "origin": {"source": "homepage", "file": "services.yaml", "group": "Media", "entry": "Sonarr"}
//	  },
//	  "service@docker:i_d/container/immich_server": {
//	    "name": "Immich", "href": "https://photos.lab.example",
//	    "bind": {"resource": "docker:i_d/container/immich_server"},
//	    "origin": {"source": "labels", "group": "Apps", "entry": "Immich"}
//	  }
//	}
type Tiles map[string]Tile

// Tile is one item's configuration. Every field is optional: an item with no
// entry shows its widget's defaults.
type Tile struct {
	// Name replaces the widget's own title.
	Name string `json:"name,omitempty"`
	// Icon is the source's icon name or URL (Homepage's "sonarr.png",
	// "mdi-…", or a link), kept for when the hub caches icons.
	Icon string `json:"icon,omitempty"`
	// Href is the tile's link: http(s) only.
	Href        string `json:"href,omitempty"`
	Description string `json:"description,omitempty"`
	// Chromeless tiles have no header (a Homepage entry with no name).
	Chromeless bool     `json:"chromeless,omitempty"`
	Bind       *Binding `json:"bind,omitempty"`
	// Options are the widget's display options (free-form, per widget type).
	Options map[string]any `json:"options,omitempty"`
	// Origin says where the tile came from.
	Origin *Origin `json:"origin,omitempty"`
}

// Binding is what a tile shows.
type Binding struct {
	// Instance is an integration instance id (status: can Cha Dash read it?).
	Instance string `json:"instance,omitempty"`
	// Check names a target of the checks instance (status: is it up?).
	Check string `json:"check,omitempty"`
	// Resource is a Home Graph resource id. It overrides the target an item
	// id carries (`<type>@<resource>`).
	Resource string `json:"resource,omitempty"`
	// Sources are instances read by capability (the media calendar reads
	// Sonarr and Radarr).
	Sources []Source `json:"sources,omitempty"`
}

// Source binds a tile to a capability of an instance.
type Source struct {
	Instance   string         `json:"instance"`
	Capability string         `json:"capability"`
	Options    map[string]any `json:"options,omitempty"`
}

// Origin is where a tile came from: an import (source "homepage", with the
// file, group and entry), label discovery (source "labels", with the
// container's homepage.group and homepage.name), or nothing (added by hand).
type Origin struct {
	Source string `json:"source,omitempty"`
	File   string `json:"file,omitempty"`
	Group  string `json:"group,omitempty"`
	Entry  string `json:"entry,omitempty"`
}

// Limits on a tiles document.
const (
	MaxTiles     = 2000
	MaxTileIDLen = 512
)

// ErrInvalidTiles wraps every reason ParseTiles refuses a document.
var ErrInvalidTiles = errors.New("invalid tiles")

// InvalidError is a refused tiles document or dismissal: it unwraps to
// ErrInvalidTiles or ErrInvalidRef and says why as a catalog sentence.
type InvalidError struct {
	Err error
	M   msg.M
}

func (e *InvalidError) Error() string { return e.M.Text() }
func (e *InvalidError) Unwrap() error { return e.Err }

func invalidTiles(m msg.M) error { return &InvalidError{Err: ErrInvalidTiles, M: m} }

// ParseTiles validates a tiles document: a JSON object of item ids to tiles,
// with no unknown fields, http(s) links only, and sources that name both an
// instance and a capability.
func ParseTiles(raw json.RawMessage) (Tiles, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, invalidTiles(msg.New("boards.tiles_not_object"))
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	var tiles Tiles
	if err := dec.Decode(&tiles); err != nil {
		return nil, invalidTiles(msg.New("boards.tiles_unreadable", "reason", err.Error()))
	}
	if dec.More() {
		return nil, invalidTiles(msg.New("boards.tiles_trailing"))
	}
	if len(tiles) > MaxTiles {
		return nil, invalidTiles(msg.New("boards.tiles_too_many", "count", len(tiles), "max", MaxTiles))
	}
	for id, t := range tiles {
		if id == "" || len(id) > MaxTileIDLen || !utf8.ValidString(id) {
			return nil, invalidTiles(msg.New("boards.tiles_bad_id", "max", MaxTileIDLen))
		}
		if t.Href != "" && !HrefOK(t.Href) {
			return nil, invalidTiles(msg.New("boards.tiles_bad_href", "id", id))
		}
		if t.Bind != nil {
			for _, s := range t.Bind.Sources {
				if s.Instance == "" || s.Capability == "" {
					return nil, invalidTiles(msg.New("boards.tiles_bad_source", "id", id))
				}
			}
		}
	}
	return tiles, nil
}

// HrefOK reports whether a tile link is acceptable: it starts with http://
// or https://, in any case. The 00004 migration and the web app
// (stores/tiles.ts) apply exactly this rule, so a link one of them keeps is
// never refused by another.
func HrefOK(href string) bool {
	lower := strings.ToLower(href)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}
