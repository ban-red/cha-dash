package export

import (
	"errors"
	"strings"
	"testing"
)

const head = "apiVersion: chadash/v1alpha1\nkind: Home\n"

func TestParseRefusesWhatHomeFilesNeverHold(t *testing.T) {
	plaintext := secretValues["sonarr"]
	for name, tc := range map[string]struct{ file, want string }{
		"empty":            {"", "the file is empty"},
		"comments only":    {"# nothing\n", "the file is empty"},
		"not YAML":         {"a: [b\n", "isn't valid YAML"},
		"two documents":    {head + "---\n" + head, "one YAML document"},
		"not a Home file":  {"boards: []\n", "needs apiVersion: chadash/v1alpha1 and kind: Home"},
		"another version":  {"apiVersion: chadash/v2\nkind: Home\n", "apiVersion: must be chadash/v1alpha1"},
		"another kind":     {"apiVersion: chadash/v1alpha1\nkind: Integration\n", "kind: must be Home"},
		"unknown field":    {head + "users: []\n", "line 3, users: isn't a field a Home file has"},
		"repeated field":   {head + "boards: []\nboards: []\n", "boards: is repeated"},
		"aliases":          {head + "meta: &m {hub_version: x}\nsettings: {import/a: *m}\n", "aliases aren't supported"},
		"board without id": {head + "boards:\n- {name: X, slug: x}\n", "boards[0]: needs id, name and slug"},
		"duplicate board":  {head + "boards:\n- {id: b_x, name: X, slug: x}\n- {id: b_x, name: Y, slug: y}\n", "boards[1].id: is used by another board"},
		"duplicate slug":   {head + "boards:\n- {id: b_x, name: X, slug: x}\n- {id: b_y, name: Y, slug: x}\n", "boards[1].slug: is used by another board"},
		"bad slug":         {head + "boards:\n- {id: b_x, name: X, slug: 'Not A Slug'}\n", "boards[0].slug: must be lowercase"},
		"bad id":           {head + "instances:\n- {id: '../x', integration: keyed, name: X}\n", "instances[0].id: must be an id"},
		"layout not object": {head + "boards:\n- {id: b_x, name: X, slug: x, layout: {format_version: 1, document: [1]}}\n",
			"boards[0].layout.document: must be a mapping"},
		"duplicate instance name": {head + "instances:\n- {id: i_a, integration: keyed, name: X}\n- {id: i_b, integration: keyed, name: x}\n",
			"instances[1].name: is used by another keyed instance"},
		"plaintext secret": {head + "instances:\n- id: i_x\n  integration: keyed\n  name: X\n  secrets:\n    api_key: " + plaintext + "\n",
			"line 8, instances[0].secrets.api_key: must be a placeholder"},
		"quoted plaintext secret": {head + "instances:\n- {id: i_x, integration: keyed, name: X, secrets: {api_key: '" + plaintext + "'}}\n",
			"instances[0].secrets.api_key: must be a placeholder"},
		"secret as a mapping": {head + "instances:\n- {id: i_x, integration: keyed, name: X, secrets: {api_key: {value: '" + plaintext + "'}}}\n",
			"instances[0].secrets.api_key: must be a placeholder"},
		"bad vault name": {head + "instances:\n- {id: i_x, integration: keyed, name: X, secrets: {api_key: !secret '../x'}}\n",
			"names a vault entry that isn't valid"},
		"placeholder in config": {head + "instances:\n- {id: i_x, integration: keyed, name: X, config: {url: !secret a/b}}\n",
			"instances[0].config.url: a !secret placeholder belongs under an instance's secrets"},
		"reference in config": {head + "instances:\n- {id: i_x, integration: keyed, name: X, config: {headers: {X-Key: '${secret:a/b}'}}}\n",
			"instances[0].config.headers.X-Key: can't hold a secret reference"},
		"tls twice": {head + "instances:\n- {id: i_x, integration: keyed, name: X, tls: {mode: verify}, config: {tls: {mode: insecure}}}\n",
			"instances[0].config.tls: is given twice"},
		"bad tls": {head + "instances:\n- {id: i_x, integration: keyed, name: X, tls: {mode: sometimes}}\n", "instances[0].tls:"},
		"infinite number": {head + "instances:\n- {id: i_x, integration: keyed, name: X, config: {n: .inf}}\n",
			"instances[0].config.n: must be a number JSON can hold"},
		"custom tag":      {head + "settings: {import/a: !include x.yaml}\n", "settings.import/a: uses a YAML tag"},
		"bad position":    {head + "boards:\n- {id: b_x, name: X, slug: x, position: first}\n", "boards[0].position: must be a whole number"},
		"bad enabled":     {head + "instances:\n- {id: i_x, integration: keyed, name: X, enabled: maybe}\n", "instances[0].enabled: must be true or false"},
		"broken bundle":   {head + "secrets_bundle: {cipher: xchacha20poly1305}\n", "secrets_bundle: needs cipher, kdf, nonce, names and ciphertext"},
		"control in name": {head + "boards:\n- {id: b_x, name: \"a\\tb\", slug: x}\n", "boards[0].name: must be 1–64 characters"},
	} {
		_, err := Parse([]byte(tc.file))
		var pe *ParseError
		if !errors.As(err, &pe) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want …%s…", name, err, tc.want)
			continue
		}
		if strings.Contains(err.Error(), plaintext) {
			t.Errorf("%s: the error quotes the secret: %v", name, err)
		}
	}
}

func TestParseAcceptsHandWrittenFiles(t *testing.T) {
	d := parse(t, []byte(head+`
instances:
  - id: i_x
    integration: keyed
    name: "  Sonarr  "
    config:
      url: https://sonarr.home.arpa
      tls: {mode: tofu}       # the stored shape: tls inside config
      port: 0x1F              # YAML ints that JSON spells differently
      ratio: 1.50
      big: 123456789012345678901234567890
      when: 2026-10-04
      quoted: "8006"
    secrets:
      api_key: ${secret:shared/sonarr}
`))
	in := d.Instances[0]
	if in.Name != "Sonarr" || !in.Enabled || string(in.TLS) != `{"mode":"tofu"}` || in.Secrets["api_key"] != "shared/sonarr" {
		t.Fatalf("instance = %+v", in)
	}
	if want := `{"url":"https://sonarr.home.arpa","port":31,"ratio":1.50,"big":123456789012345678901234567890,"when":"2026-10-04","quoted":"8006"}`; string(in.Config) != want {
		t.Fatalf("config = %s\nwant     %s", in.Config, want)
	}
	if len(d.Boards) != 0 || len(d.Settings) != 0 || d.Bundle != nil {
		t.Fatalf("document = %+v", d)
	}
}

func TestEncodeKeepsOrderAndText(t *testing.T) {
	d := &Document{Boards: []Board{{ID: "b_x", Name: "X", Slug: "x", Layout: &Layout{FormatVersion: 1,
		Document: []byte(`{"version":1,"columns":12,"sections":[{"id":"s","title":"true","span":4,"items":[]}],"note":"line one\nline two","n":1.50}`)}}},
		Settings: []Setting{{Key: "import/x", Value: []byte(`{"z":1,"a":[1,2,3],"m":{"k":null,"t":false}}`)}}}
	data, err := Encode(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"        version: 1\n        columns: 12\n        sections:\n          - {id: s, title: \"true\", span: 4, items: []}\n        note: |-\n          line one\n          line two\n        n: 1.50\n",
		"  import/x:\n    z: 1\n    a: [1, 2, 3]\n    m: {k: null, t: false}\n",
		"instances: []\n",
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("lacks %q:\n%s", want, data)
		}
	}
	back := parse(t, data)
	if string(back.Boards[0].Layout.Document) != string(d.Boards[0].Layout.Document) || string(back.Settings[0].Value) != string(d.Settings[0].Value) {
		t.Fatalf("round trip changed JSON:\n%s\n%s", back.Boards[0].Layout.Document, back.Settings[0].Value)
	}
}
