# Plain YAML boards

A board written by hand, for people with no dashboard to import, or who'd rather write a file than fill in ten forms. It's a front end to the same plan every importer makes, so what lands is the layout document and tiles document a Home file carries. It leaves out what a person shouldn't have to write: IDs, coordinates, supported sizes, TLS pins.

```yaml
apiVersion: chadash/v1alpha1
kind: Board
name: Home
sections:
  - title: Media
    span: 8                      # of 12; default 4
    tiles:
      - name: Sonarr
        href: https://sonarr.home.example
        connect: sonarr          # an integration ID; the tile reads that connection
        url: https://sonarr.home.example
        api_key: 0123abcd…       # moved into the vault; the preview says to remove it from the file
        size: wide               # optional; the widget's default otherwise
        widget: arr              # optional; the integration's catalog widget otherwise
      - name: Plex
        connect: plex
        url: https://plex.home.example
        token: ${PLEX_TOKEN}     # not resolved: "key needed", like a Homepage variable
  - title: Links
    tiles:
      - {name: Wiki, href: https://wiki.home.example}                 # a link tile, with an http check
      - {name: Notes, href: https://notes.home.example, check: false}  # no check
      - {name: Printer, href: http://printer.lan, check: "http://10.20.0.7:631"}
```

## Rules

- **Keys.** The board takes `apiVersion`, `kind`, `name`, `sections`; a section `title`, `span`, `tiles`; a tile `name`, `href`, `icon`, `description`, `size`, `widget`, `connect`, `check`, `options`. Any other key on a tile that connects is a config field of that connection. An unknown key anywhere else is an error naming its line.
- **Order is position.** Tiles flow in file order through the layout engine's reflow, as with Homepage. Coordinates aren't accepted; arrange the board in the app.
- **IDs are derived** from the board, section and tile names, so importing the same file again changes nothing, and renaming a tile is a new tile plus a stale one.
- **Connections are shared.** Tiles with the same `connect` and `url` use one connection, named after the first tile.
- **Secrets.** A value in a secret field moves into the vault, and the Review sheet's hints say which fields to remove from the file. `${VAR}` is never resolved: in a secret field it's a key to add; elsewhere the field is left out.
- **Widgets** are the ones an importer can place (`internal/importer/kit.go`'s `WidgetSizes`).
- **Errors** name the file and line, never the text there. Aliases are refused.
- **One board per file.** A `kind: Boards` list is a follow-up.

Why not the Home file's own board types? `internal/export` keeps a board's layout and tiles as opaque JSON, so there's nothing typed to share; compiling to the importer's plan gives the same documents, and a change to them breaks this package's tests.
