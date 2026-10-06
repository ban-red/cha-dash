package integration

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const sampleRecipe = "---\n" + `title: A read-only token
summary: A user and a token that can only read.
access: read-only
params:
  - name: user
    label: User
    default: chadash@pve
    pattern: '^[A-Za-z0-9._-]+@(pve|pam)$'
  - name: token
    label: Token name
    default: dash
  - name: url
    label: Address
    from: config.url
    default: https://pve.lab.example
    pattern: '^https?://[A-Za-z0-9.-]+(:[0-9]+)?$'
  - name: instance
    label: Name
    from: name
    default: pve1
    pattern: '^[a-z0-9][a-z0-9-]{0,62}$'
  - name: privs
    label: Version
    options:
      - {label: '9.x', value: Sys.Audit VM.Audit}
      - {label: '8.4', value: Sys.Audit}
fills:
  token_id: '{{user}}!{{token}}'
grants: [Sys.Audit, VM.Audit]
discover:
  - {scheme: https, port: 8006, path: /, match: Proxmox Virtual Environment}
---

## Create the user

On a node, as root. The **role** holds ` + "`Sys.Audit`" + `
and nothing else.

` + "```sh" + `
pveum user add {{user}} --comment "{{instance}}"
pveum role add R --privs "{{privs}}"
docker network inspect x --format '{{.Subnet}}'
` + "```" + `

1. Open {{url}}.
2. Press **Add**,
  then save.

- one
- two

## Notes

- A note.
`

func TestParseRecipe(t *testing.T) {
	r, err := ParseRecipe([]byte(sampleRecipe))
	if err != nil {
		t.Fatal(err)
	}
	if r.Title != "A read-only token" || r.Access != RecipeReadOnly || len(r.Params) != 5 || len(r.Steps) != 1 {
		t.Fatalf("recipe = %+v", r)
	}
	if r.Params[1].Pattern != DefaultParamPattern {
		t.Errorf("a param without a pattern gets the default: %q", r.Params[1].Pattern)
	}
	if r.Params[4].Default != "Sys.Audit VM.Audit" || r.Params[4].Pattern != "" {
		t.Errorf("a param with options defaults to the first: %+v", r.Params[4])
	}
	want := []RecipeBlock{
		{Kind: BlockText, Text: "On a node, as root. The **role** holds `Sys.Audit` and nothing else."},
		{Kind: BlockCode, Lang: "sh", Code: "pveum user add {{user}} --comment \"{{instance}}\"\npveum role add R --privs \"{{privs}}\"\ndocker network inspect x --format '{{.Subnet}}'"},
		{Kind: BlockList, Ordered: true, Items: []string{"Open {{url}}.", "Press **Add**, then save."}},
		{Kind: BlockList, Items: []string{"one", "two"}},
	}
	if !reflect.DeepEqual(r.Steps[0].Blocks, want) {
		t.Fatalf("blocks =\n%#v\nwant\n%#v", r.Steps[0].Blocks, want)
	}
	if len(r.Notes) != 1 || r.Notes[0].Items[0] != "A note." {
		t.Fatalf("notes = %+v", r.Notes)
	}
	if len(r.Discover) != 1 || r.Discover[0].Port != 8006 {
		t.Fatalf("discover = %+v", r.Discover)
	}

	// The wire shape: discover stays in the hub, empty lists are lists.
	b, _ := json.Marshal(r)
	for _, s := range []string{`"access":"read-only"`, `"fills":{"token_id":"{{user}}!{{token}}"}`, `"from":"config.url"`, `"kind":"code","lang":"sh"`} {
		if !strings.Contains(string(b), s) {
			t.Errorf("JSON lacks %s:\n%s", s, b)
		}
	}
	if strings.Contains(string(b), "discover") || strings.Contains(string(b), "8006") {
		t.Errorf("discover hints leaked into the JSON: %s", b)
	}
	none, err := ParseRecipe([]byte("---\ntitle: T\nsummary: S\naccess: none\n---\n## Step\n\nText.\n"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(none)
	if !strings.Contains(string(b), `"params":[],`) || !strings.Contains(string(b), `"notes":[],"fills":{},"grants":[]`) {
		t.Errorf("empty fields should be [] and {}: %s", b)
	}
}

func TestBoundOptions(t *testing.T) {
	src := "---\ntitle: T\nsummary: S\naccess: read-only\nparams:\n" +
		"  - name: scopes\n    label: Scopes\n    from: config.mode\n    default: 'a:read_any'\n    options:\n" +
		"      - {label: Read-only, value: 'a:read_any', when: read_only}\n" +
		"      - {label: Control, value: 'a:read_any,a:update_any', when: control}\n" +
		"---\n## X\n\n```url\nhttps://x.example/?scopes={{scopes}}\n```\n"
	r, err := ParseRecipe([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(r.Params)
	if !strings.Contains(string(b), `"options":[{"label":"Read-only","value":"a:read_any","when":"read_only"},{"label":"Control","value":"a:read_any,a:update_any","when":"control"}],"from":"config.mode"`) {
		t.Fatalf("params = %s", b)
	}
}

func TestParseRecipeErrors(t *testing.T) {
	base := "---\ntitle: T\nsummary: S\naccess: read-only\n%s---\n%s"
	param := func(p string) string { return "params:\n  - " + strings.ReplaceAll(p, "\n", "\n    ") + "\n" }
	cases := map[string][2]string{
		"no front matter":    {"", "## X\n\nY\n"},
		"unknown field":      {"colour: red\n", "## X\n\nY\n"},
		"bad access":         {"", ""},
		"no steps":           {"", ""},
		"text before":        {"", "Intro.\n## X\n\nY\n"},
		"h3":                 {"", "## X\n\n### Y\n"},
		"unclosed code":      {"", "## X\n\n```sh\nls\n"},
		"notes not last":     {"", "## Notes\n\n- n\n\n## X\n\nY\n"},
		"empty step":         {"", "## X\n\n## Y\n\nZ\n"},
		"undeclared":         {"", "## X\n\nrun {{nope}}\n"},
		"unused param":       {param("name: user\nlabel: U\ndefault: a"), "## X\n\nY\n"},
		"unanchored":         {param("name: user\nlabel: U\ndefault: a\npattern: '[a-z]+'"), "## X\n\n{{user}}\n"},
		"lets ; through":     {param("name: user\nlabel: U\ndefault: a\npattern: '^.+$'"), "## X\n\n{{user}}\n"},
		"lets space through": {param("name: user\nlabel: U\ndefault: a\npattern: '^[a-z ]+$'"), "## X\n\n{{user}}\n"},
		"default mismatch":   {param("name: user\nlabel: U\ndefault: A\npattern: '^[a-z]+$'"), "## X\n\n{{user}}\n"},
		"no label":           {param("name: user\ndefault: a"), "## X\n\n{{user}}\n"},
		"bad from":           {param("name: u\nlabel: U\ndefault: a\nfrom: secrets.key"), "## X\n\n{{u}}\n"},
		"option default":     {param("name: v\nlabel: V\ndefault: z\noptions:\n  - {label: A, value: a}"), "## X\n\n{{v}}\n"},
		"bound, no when":     {param("name: v\nlabel: V\nfrom: config.mode\ndefault: a\noptions:\n  - {label: A, value: a}"), "## X\n\n{{v}}\n"},
		"when, unbound":      {param("name: v\nlabel: V\ndefault: a\noptions:\n  - {label: A, value: a, when: x}"), "## X\n\n{{v}}\n"},
		"when twice":         {param("name: v\nlabel: V\nfrom: config.mode\ndefault: a\noptions:\n  - {label: A, value: a, when: x}\n  - {label: B, value: b, when: x}"), "## X\n\n{{v}}\n"},
		"bad fill field":     {"fills:\n  Token-ID: x\n", "## X\n\nY\n"},
		"bad hint":           {"discover:\n  - {scheme: ftp, port: 21, path: /, match: x}\n", "## X\n\nY\n"},
	}
	for name, c := range cases {
		src := strings.Replace(base, "%s", c[0], 1)
		src = strings.Replace(src, "%s", c[1], 1)
		switch name {
		case "no front matter":
			src = c[1]
		case "bad access":
			src = "---\ntitle: T\nsummary: S\naccess: some\n---\n## X\n\nY\n"
		}
		if _, err := ParseRecipe([]byte(src)); err == nil {
			t.Errorf("%s: parsed without an error:\n%s", name, src)
		}
	}
}

func TestRenderAndPlaceholders(t *testing.T) {
	s := "pveum acl modify / --tokens '{{user}}!{{token}}' {{user}} {{.Subnet}} {{ spaced }}"
	if got := Placeholders(s); !reflect.DeepEqual(got, []string{"user", "token"}) {
		t.Fatalf("placeholders = %v", got)
	}
	got := Render(s, map[string]string{"user": "chadash@pve"})
	if got != "pveum acl modify / --tokens 'chadash@pve!{{token}}' chadash@pve {{.Subnet}} {{ spaced }}" {
		t.Fatalf("render = %q", got)
	}
}

func TestRegistryRecipes(t *testing.T) {
	r := NewRegistry()
	rc, err := ParseRecipe([]byte(sampleRecipe))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.AddRecipe("proxmox", rc); err != nil {
		t.Fatal(err)
	}
	if err := r.AddRecipe("proxmox", rc); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("a second recipe: %v", err)
	}
	if err := r.AddRecipe("Bad ID", rc); err == nil {
		t.Fatal("an invalid id should fail")
	}
	if err := r.AddRecipe("pbs", Recipe{Title: "T", Summary: "S", Access: "?"}); err == nil {
		t.Fatal("an invalid recipe should fail")
	}
	if got, ok := r.Recipe("proxmox"); !ok || got.Title != rc.Title {
		t.Fatalf("recipe = %+v %v", got, ok)
	}
	if _, ok := r.Recipe("pbs"); ok {
		t.Fatal("pbs has no recipe")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("RegisterRecipe should panic on a bad recipe")
		}
	}()
	RegisterRecipe("zzbad", []byte("not a recipe"))
}
