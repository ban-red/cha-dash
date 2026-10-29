package importer

import "dash.cha.sh/internal/boards"

// TileConfigs is the board's tiles document for a plan: what Apply writes
// next to the layout. It's the plan's tiles without what the layout already
// says (section, widget type, size), keyed by item id. Links that aren't
// http(s) are dropped, as the boards API would refuse them; migration 00004
// converts the tiles an older import record held the same way.
func TileConfigs(p *Plan) boards.Tiles {
	out := make(boards.Tiles, len(p.Tiles)+len(p.baseTiles))
	for _, t := range p.Tiles {
		c := boards.Tile{
			Name:        t.Name,
			Icon:        t.Icon,
			Description: t.Description,
			Chromeless:  t.Chromeless,
			Options:     t.Options,
			Origin:      &boards.Origin{Source: p.Source, File: t.Origin.File, Group: t.Origin.Group, Entry: t.Origin.Entry},
		}
		if boards.HrefOK(t.Href) {
			c.Href = t.Href
		}
		if t.Instance != "" || t.Check != "" || len(t.Sources) > 0 {
			c.Bind = &boards.Binding{Instance: t.Instance, Check: t.Check}
			for _, s := range t.Sources {
				c.Bind.Sources = append(c.Bind.Sources, boards.Source{Instance: s.Instance, Capability: s.Capability, Options: s.Options})
			}
		}
		out[t.ItemID] = c
	}
	// Adding to a board: what's already on it stays as the person left it.
	for id, t := range p.baseTiles {
		out[id] = t
	}
	return out
}
