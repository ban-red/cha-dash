package export

import (
	"bytes"
	"encoding/json"
	"fmt"

	"dash.cha.sh/internal/boards"
)

// Tile config: a board's tiles document (internal/boards/tiles.go), what
// each layout item shows that the layout can't hold, keyed by item id. The
// hub stores it with every layout revision, so a revision is a layout and
// its tiles together. Everything this package knows about tiles is in this
// file.
//
// In a Home file it's boards[].tiles, written when the board has any. On
// import, a board without a tiles key keeps the tiles it has (as the boards
// API does when a save leaves tiles out); `tiles: {}` clears them. Tiles
// reference instances by ID (bind.instance, bind.sources[].instance); an
// import never renumbers instances, so they need no remapping, and a board
// matched by slug takes the file's tiles with it.

// exportTiles is a board's tiles for the file, or nil when it has none.
func exportTiles(d boards.Detail) json.RawMessage {
	t := bytes.TrimSpace(d.Layout.Tiles)
	if d.Layout.Document == nil || len(t) == 0 || bytes.Equal(t, []byte(boards.EmptyTiles)) {
		return nil
	}
	return d.Layout.Tiles
}

// tileWork is a board's tiles in the file compared with the hub's.
type tileWork struct {
	change *Change         // nil: the file says nothing about tiles
	write  json.RawMessage // non-nil: the tiles the next revision carries
}

// planTiles compares the tiles the file has for a board (want; nil when it
// has no tiles key) with the hub's. cur is the hub's board when exists.
// Tiles that don't pass the hub's own rules refuse the file.
func planTiles(path string, cur boards.Detail, exists bool, boardID, name string, want json.RawMessage) (tileWork, error) {
	if want == nil {
		return tileWork{}, nil
	}
	tiles, err := boards.ParseTiles(want)
	if err != nil {
		return tileWork{}, &ParseError{Path: path, Msg: err.Error()}
	}
	c := &Change{}
	c.Kind, c.ID, c.Name = "tiles", boardID, name
	var have json.RawMessage
	if exists && cur.Layout.Document != nil {
		have = cur.Layout.Tiles
	}
	switch {
	case have == nil && len(tiles) == 0, have != nil && sameRaw(have, want):
		c.Action = actionUnchanged
		return tileWork{change: c}, nil
	case have == nil:
		c.Action = actionCreate
		c.Detail = plural(len(tiles), "tile")
	default:
		c.Action = actionUpdate
		if err := c.diffRaw("", have, want); err != nil {
			return tileWork{}, fmt.Errorf("export: board %s: tiles: %w", boardID, err)
		}
	}
	return tileWork{change: c, write: want}, nil
}
