// Package homarr imports a Homarr 2 backup: the zip from Management → Tools
// → Backup, which holds Homarr's whole SQLite database (db.sqlite) and a
// metadata.json. The parser opens the database read-only from a temporary
// file and maps one board (the one with the most items) onto a plan.
//
// Homarr encrypts every integration secret with its own
// SECRET_ENCRYPTION_KEY, so none can be read here: each connection arrives
// with its secret fields missing, and the Review sheet's key panel (or the
// connection's form later) fills them. Mapping table in README.md.
package homarr

import (
	"archive/zip"
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"dash.cha.sh/internal/importer"
	"dash.cha.sh/internal/store"
)

// Source names this importer: /api/import/homarr, settings["import/homarr"].
const Source = "homarr"

// Limits: the zip as sent, and the database inside it. Homarr's database
// carries its icon index, so it's bigger than a dashboard's config.
const (
	MaxZipBytes = 24 << 20
	maxDBBytes  = 256 << 20
)

// ErrNotABackup is a zip without Homarr's db.sqlite.
var ErrNotABackup = errors.New("homarr: this zip has no db.sqlite; make the backup in Homarr under Management → Tools → Backup")

// integrationSpec maps a Homarr integration kind onto a Cha Dash
// integration and the tile it feeds.
type integrationSpec struct {
	integration string
	tile        string
}

var integrationSpecs = map[string]integrationSpec{
	"sonarr":        {"sonarr", "arr"},
	"radarr":        {"radarr", "arr"},
	"lidarr":        {"lidarr", "arr"},
	"prowlarr":      {"prowlarr", "service"},
	"plex":          {"plex", "plex"},
	"jellyfin":      {"jellyfin", "plex"},
	"jellyseerr":    {"seerr", "service"},
	"overseerr":     {"seerr", "service"},
	"piHole":        {"pihole", "service"},
	"adGuardHome":   {"adguard", "service"},
	"homeAssistant": {"homeassistant", "homeassistant"},
	"proxmox":       {"proxmox", "service"},
	"qBittorrent":   {"qbittorrent", "downloads"},
	"transmission":  {"transmission", "downloads"},
	"nzbGet":        {"nzbget", "downloads"},
	"sabNzbd":       {"sabnzbd", "downloads"},
	"nextcloud":     {"nextcloud", "service"},
	"immich":        {"immich", "service"},
	"unraid":        {"unraid", "service"},
	"mealie":        {"mealie", "service"},
	"tautulli":      {"tautulli", "service"},
	"bazarr":        {"bazarr", "service"},
}

// widgetTiles maps Homarr item kinds that read an integration onto the
// Cha Dash tile that shows the same thing.
var widgetTiles = map[string]string{
	"calendar":                    "calendar",
	"downloads":                   "downloads",
	"mediaServer":                 "plex",
	"dnsHoleSummary":              "service",
	"dnsHoleControls":             "service",
	"smartHome-entityState":       "homeassistant",
	"smartHome-executeAutomation": "homeassistant",
	"mediaRequests-requestList":   "service",
	"mediaRequests-requestStats":  "service",
	"indexerManager":              "service",
	"healthMonitoring":            "service",
}

// calendarSources are the integrations the media calendar reads.
var calendarSources = map[string]bool{"sonarr": true, "radarr": true, "lidarr": true}

// Build reads a backup zip and maps it with cat (the hub's integrations).
func Build(zipBytes []byte, cat importer.Catalog) (*importer.Result, importer.Secrets, error) {
	db, cleanup, err := openBackup(zipBytes)
	if err != nil {
		return nil, nil, err
	}
	defer cleanup()
	return build(context.Background(), db, cat)
}

func openBackup(zipBytes []byte) (*sql.DB, func(), error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, nil, fmt.Errorf("homarr: the file isn't a zip: %w", err)
	}
	var f *zip.File
	for _, zf := range zr.File {
		if path.Base(zf.Name) == "db.sqlite" {
			f = zf
			break
		}
	}
	if f == nil {
		return nil, nil, ErrNotABackup
	}
	rc, err := f.Open()
	if err != nil {
		return nil, nil, fmt.Errorf("homarr: db.sqlite: %w", err)
	}
	defer rc.Close()
	dir, err := os.MkdirTemp("", "chadash-homarr-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	p := filepath.Join(dir, "db.sqlite")
	out, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err == nil {
		var n int64
		n, err = io.Copy(out, io.LimitReader(rc, maxDBBytes+1))
		if err == nil && n > maxDBBytes {
			err = fmt.Errorf("homarr: db.sqlite is larger than %d MB", maxDBBytes>>20)
		}
		err = errors.Join(err, out.Close())
	}
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	db, err := store.OpenForeign(p)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return db, func() { _ = db.Close(); cleanup() }, nil
}

type section struct {
	id, kind, name string
	x, y           int
}

type item struct {
	id, kind, options, section string
	x, y, w, h                 int
}

type app struct{ name, description, icon, href, ping string }

type integ struct{ id, name, url, kind, appID string }

func build(ctx context.Context, db *sql.DB, cat importer.Catalog) (*importer.Result, importer.Secrets, error) {
	if !hasTable(ctx, db, "board") || !hasTable(ctx, db, "item") {
		return nil, nil, errors.New("homarr: db.sqlite has no boards; is this a Homarr 1.0 or later backup?")
	}
	boardID, boardName, others, err := pickBoard(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	k := importer.NewKit(Source)
	for _, o := range others {
		k.Ignore(importer.Ignored{File: "db.sqlite", Key: "board " + o, Reason: "one board per import; import again to bring this one in"})
	}

	sections, err := loadSections(ctx, db, boardID)
	if err != nil {
		return nil, nil, err
	}
	items, err := loadItems(ctx, db, boardID)
	if err != nil {
		return nil, nil, err
	}
	apps, err := loadApps(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	integs, byItem, err := loadIntegrations(ctx, db)
	if err != nil {
		return nil, nil, err
	}

	ids := make([]string, 0, len(integs))
	for id := range integs {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	// Connections, created as items need them, keyed by Homarr's ID.
	made := map[string]*importer.Instance{}
	refused := map[string]bool{}
	connect := func(in integ, origin importer.Origin) (string, string) {
		if refused[in.id] {
			return "", ""
		}
		spec, ok := integrationSpecs[in.kind]
		secret, required, have := []string(nil), []string(nil), false
		if ok {
			secret, required, have = cat.Integration(spec.integration)
		}
		if !ok || !have {
			refused[in.id] = true
			k.Unmapped(importer.Unmapped{Kind: "integration", Origin: origin, Type: in.kind, Reason: "Cha Dash has no integration for it yet"})
			return "", ""
		}
		if m := made[in.id]; m != nil {
			return m.ID, spec.tile
		}
		cfg := map[string]any{"url": in.url, "tls": importer.TLSFor(in.url)}
		inst := k.Instance(importer.Instance{ID: k.ID("i_", "integration", in.id), Integration: spec.integration, Name: cmp.Or(strings.TrimSpace(in.name), in.kind),
			Config: cfg, Origin: importer.Origin{File: "db.sqlite", Group: "integrations", Entry: in.name}})
		for _, f := range secret {
			if slices.Contains(required, f) {
				k.Missing(inst, f)
			}
		}
		for _, f := range required {
			if f != "url" && f != "tls" && !slices.Contains(secret, f) {
				k.Unmapped(importer.Unmapped{Kind: "field", Origin: inst.Origin, Type: spec.integration, Fields: []string{f},
					Reason: "Homarr keeps it encrypted; set it on the connection in Cha Dash"})
			}
		}
		made[in.id] = inst
		return inst.ID, spec.tile
	}

	secs := map[string]*importer.KitSection{}
	for _, s := range sections {
		title := s.name
		if s.kind != "category" {
			title = ""
		}
		secs[s.id] = k.Section(cmp.Or(s.name, s.kind+"-"+s.id), title, importer.LayoutColumns)
	}
	var loose *importer.KitSection
	for _, it := range items {
		sec := secs[it.section]
		if sec == nil {
			if loose == nil {
				loose = k.Section("board", "", importer.LayoutColumns)
			}
			sec = loose
		}
		opts := superJSON(it.options)
		origin := importer.Origin{File: "db.sqlite", Group: sec.Title, Entry: it.kind}
		size := sizeFor(it.w, it.h)
		switch it.kind {
		case "app":
			appID, _ := opts["appId"].(string)
			a, ok := apps[appID]
			if !ok {
				k.Unmapped(importer.Unmapped{Kind: "entry", Origin: origin, Type: "app", Reason: "the app it shows isn't in the backup"})
				continue
			}
			origin.Entry = a.name
			t := importer.Tile{Widget: "service", Name: a.name, Icon: a.icon, Description: a.description, Origin: origin}
			if importerHref(a.href) {
				t.Href = a.href
			}
			// The app's own integration, if one points at it.
			for _, iid := range ids {
				if in := integs[iid]; in.appID == appID {
					if id, _ := connect(in, origin); id != "" {
						t.Instance = id
					}
					break
				}
			}
			if t.Instance == "" {
				t.Check = k.Check(a.name, cmp.Or(a.ping, a.href))
			}
			k.Tile(sec, t, size)
		case "clock", "weather", "bookmarks":
			if it.kind == "weather" {
				k.Unmapped(importer.Unmapped{Kind: "widget", Origin: origin, Type: it.kind, Reason: "add the Weather integration in Cha Dash, then the Weather tile"})
				continue
			}
			if it.kind == "bookmarks" {
				k.Unmapped(importer.Unmapped{Kind: "widget", Origin: origin, Type: it.kind, Reason: "bookmarks don't come across yet; their apps do, as tiles"})
				continue
			}
			k.Tile(sec, importer.Tile{Widget: "clock", Name: "Clock", Origin: origin}, size)
		default:
			tile, ok := widgetTiles[it.kind]
			if !ok {
				k.Unmapped(importer.Unmapped{Kind: "widget", Origin: origin, Type: it.kind, Reason: "no Cha Dash tile shows this yet"})
				continue
			}
			t := importer.Tile{Widget: tile, Name: kindName(it.kind), Origin: origin}
			for _, iid := range byItem[it.id] {
				in, ok := integs[iid]
				if !ok {
					continue
				}
				id, _ := connect(in, origin)
				if id == "" {
					continue
				}
				if tile == "calendar" {
					spec := integrationSpecs[in.kind]
					if calendarSources[spec.integration] {
						t.Sources = append(t.Sources, importer.Source{Instance: id, Capability: "media.calendar"})
					}
					continue
				}
				if t.Instance == "" {
					t.Instance = id
					t.Name = cmp.Or(in.name, t.Name)
				}
			}
			if t.Instance == "" && len(t.Sources) == 0 {
				k.Unmapped(importer.Unmapped{Kind: "widget", Origin: origin, Type: it.kind, Reason: "it reads no integration Cha Dash can connect"})
				continue
			}
			k.Tile(sec, t, size)
		}
	}
	// Integrations no item uses still come across as connections.
	for _, id := range ids {
		if made[id] == nil {
			connect(integs[id], importer.Origin{File: "db.sqlite", Group: "integrations", Entry: integs[id].name})
		}
	}
	if len(made) > 0 {
		k.Note("Homarr keeps integration secrets encrypted with its own key, so every connection needs its key again. Add them in this preview, or later on each connection.")
	}
	return k.Finish(boardID, cmp.Or(boardName, "Homarr"), "Homarr")
}

func importerHref(h string) bool {
	return strings.HasPrefix(h, "http://") || strings.HasPrefix(h, "https://")
}

func kindName(kind string) string {
	switch kind {
	case "calendar":
		return "Up next"
	case "downloads":
		return "Downloads"
	case "mediaServer":
		return "Now playing"
	}
	return kind
}

// sizeFor turns Homarr's grid cells into a Cha Dash size; the kit fits it to
// what the widget supports.
func sizeFor(w, h int) string {
	switch {
	case w >= 4 && h >= 4:
		return "large"
	case w >= 2 && h >= 4:
		return "tall"
	case w >= 2:
		return "wide"
	}
	return "small"
}

// superJSON reads SuperJSON's {"json": …} wrapper, or plain JSON.
func superJSON(s string) map[string]any {
	var v map[string]any
	if json.Unmarshal([]byte(s), &v) != nil {
		return map[string]any{}
	}
	if j, ok := v["json"].(map[string]any); ok {
		return j
	}
	return v
}

func hasTable(ctx context.Context, db *sql.DB, name string) bool {
	var n int
	return db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n) == nil && n > 0
}

func hasColumn(ctx context.Context, db *sql.DB, table, col string) bool {
	var n int
	return db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, table, col).Scan(&n) == nil && n > 0
}

func pickBoard(ctx context.Context, db *sql.DB) (string, string, []string, error) {
	rows, err := db.QueryContext(ctx, `SELECT b.id, b.name, (SELECT count(*) FROM item i WHERE i.boardId = b.id) AS n FROM board b ORDER BY n DESC, b.name`)
	if err != nil {
		return "", "", nil, fmt.Errorf("homarr: reading boards: %w", err)
	}
	defer rows.Close()
	var id, name string
	var others []string
	for rows.Next() {
		var bid, bname string
		var n int
		if err := rows.Scan(&bid, &bname, &n); err != nil {
			return "", "", nil, err
		}
		if id == "" {
			id, name = bid, bname
		} else {
			others = append(others, bname)
		}
	}
	if id == "" {
		return "", "", nil, errors.New("homarr: the backup has no boards")
	}
	return id, name, others, rows.Err()
}

func loadSections(ctx context.Context, db *sql.DB, boardID string) ([]section, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, kind, COALESCE(name, ''), COALESCE(xOffset, 0), COALESCE(yOffset, 0) FROM section WHERE boardId = ?`, boardID)
	if err != nil {
		return nil, fmt.Errorf("homarr: reading sections: %w", err)
	}
	defer rows.Close()
	var out []section
	for rows.Next() {
		var s section
		if err := rows.Scan(&s.id, &s.kind, &s.name, &s.x, &s.y); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	slices.SortStableFunc(out, func(a, b section) int {
		return cmp.Or(cmp.Compare(a.y, b.y), cmp.Compare(a.x, b.x), cmp.Compare(a.id, b.id))
	})
	return out, rows.Err()
}

// loadItems reads the board's items with their places: from item_layout in
// the widest layout (Homarr 1.x with responsive layouts and later), else
// from the item's own columns (earlier 1.x).
func loadItems(ctx context.Context, db *sql.DB, boardID string) ([]item, error) {
	var q string
	switch {
	case hasTable(ctx, db, "item_layout") && hasTable(ctx, db, "layout"):
		q = `SELECT i.id, i.kind, i.options, il.sectionId, il.xOffset, il.yOffset, il.width, il.height FROM item i
			JOIN item_layout il ON il.itemId = i.id
			WHERE i.boardId = ? AND il.layoutId = (SELECT id FROM layout WHERE boardId = i.boardId ORDER BY columnCount DESC, breakpoint DESC LIMIT 1)`
	case hasColumn(ctx, db, "item", "sectionId"):
		q = `SELECT id, kind, options, sectionId, xOffset, yOffset, width, height FROM item WHERE boardId = ?`
	default:
		return nil, errors.New("homarr: this backup's items have no places; is it from Homarr 1.0 or later?")
	}
	rows, err := db.QueryContext(ctx, q, boardID)
	if err != nil {
		return nil, fmt.Errorf("homarr: reading items: %w", err)
	}
	defer rows.Close()
	var out []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.kind, &it.options, &it.section, &it.x, &it.y, &it.w, &it.h); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	slices.SortStableFunc(out, func(a, b item) int {
		return cmp.Or(cmp.Compare(a.section, b.section), cmp.Compare(a.y, b.y), cmp.Compare(a.x, b.x), cmp.Compare(a.id, b.id))
	})
	return out, rows.Err()
}

func loadApps(ctx context.Context, db *sql.DB) (map[string]app, error) {
	out := map[string]app{}
	if !hasTable(ctx, db, "app") {
		return out, nil
	}
	ping := "NULL"
	if hasColumn(ctx, db, "app", "pingUrl") {
		ping = "pingUrl"
	}
	rows, err := db.QueryContext(ctx, `SELECT id, name, COALESCE(description, ''), COALESCE(iconUrl, ''), COALESCE(href, ''), COALESCE(`+ping+`, '') FROM app`)
	if err != nil {
		return nil, fmt.Errorf("homarr: reading apps: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var a app
		if err := rows.Scan(&id, &a.name, &a.description, &a.icon, &a.href, &a.ping); err != nil {
			return nil, err
		}
		out[id] = a
	}
	return out, rows.Err()
}

func loadIntegrations(ctx context.Context, db *sql.DB) (map[string]integ, map[string][]string, error) {
	out, byItem := map[string]integ{}, map[string][]string{}
	if !hasTable(ctx, db, "integration") {
		return out, byItem, nil
	}
	appCol := "NULL"
	if hasColumn(ctx, db, "integration", "appId") {
		appCol = "appId"
	}
	rows, err := db.QueryContext(ctx, `SELECT id, name, url, kind, COALESCE(`+appCol+`, '') FROM integration`)
	if err != nil {
		return nil, nil, fmt.Errorf("homarr: reading integrations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var in integ
		if err := rows.Scan(&in.id, &in.name, &in.url, &in.kind, &in.appID); err != nil {
			return nil, nil, err
		}
		out[in.id] = in
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if !hasTable(ctx, db, "integration_item") {
		return out, byItem, nil
	}
	links, err := db.QueryContext(ctx, `SELECT itemId, integrationId FROM integration_item ORDER BY itemId, integrationId`)
	if err != nil {
		return nil, nil, fmt.Errorf("homarr: reading integration items: %w", err)
	}
	defer links.Close()
	for links.Next() {
		var itemID, integID string
		if err := links.Scan(&itemID, &integID); err != nil {
			return nil, nil, err
		}
		byItem[itemID] = append(byItem[itemID], integID)
	}
	return out, byItem, links.Err()
}
