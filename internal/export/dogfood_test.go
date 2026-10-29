package export

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/importer"
	"dash.cha.sh/internal/importer/homepage"
	"dash.cha.sh/internal/integration"
)

// TestDogfoodRoundTrip exports the hub the Homepage dogfood import makes
// (21 instances, 18 secrets, tiles from the importer) and restores it into
// a fresh hub: the same file comes back, and importing it again changes
// nothing. The registry is empty here, so every instance takes the
// unknown-integration path: references become placeholders by their shape,
// as they would for an integration this build lacks.
func TestDogfoodRoundTrip(t *testing.T) {
	ctx := t.Context()
	dir := "../../testdata/importers/homepage/dogfood"
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	res, secrets, err := homepage.Build(homepage.Input{Services: read("services.yaml"), Docker: read("docker.yaml"), Settings: read("settings.yaml")})
	if err != nil {
		t.Fatal(err)
	}
	a := newHub(t)
	a.svc = New(a.st, a.vault, Options{Registry: integration.NewRegistry(), Now: func() time.Time { return fixedNow }})
	if _, err := importer.New(a.st, a.vault).Apply(ctx, res, secrets, ""); err != nil {
		t.Fatal(err)
	}

	full, err := a.svc.Export(ctx, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := a.svc.Export(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if full.Instances != 23 || full.Secrets != 18 || full.Bundled != 18 {
		t.Fatalf("export = %d instances, %d placeholders, %d bundled", full.Instances, full.Secrets, full.Bundled)
	}
	for _, out := range [][]byte{full.Data, plain.Data} {
		if leaks := secrets.Leaks(string(out)); len(leaks) > 0 {
			t.Fatalf("the export holds %v", leaks)
		}
	}
	if !strings.Contains(string(plain.Data), "\n    tiles:\n") {
		t.Fatal("the imported board's tiles aren't in the export")
	}

	b := newHub(t)
	b.svc = New(b.st, b.vault, Options{Registry: integration.NewRegistry(), Now: func() time.Time { return fixedNow }})
	applied, err := b.svc.Apply(ctx, parse(t, full.Data), passphrase, "")
	if err != nil {
		t.Fatal(err)
	}
	if leaks := secrets.Leaks(jsonOf(t, applied)); len(leaks) > 0 {
		t.Fatalf("the result holds %v", leaks)
	}
	if applied.Plan.Summary.SecretsRestored != 18 || len(applied.Plan.SecretsNeeded) != 0 {
		t.Fatalf("summary = %+v", applied.Plan.Summary)
	}
	back, err := b.svc.Export(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if withoutMeta(back.Data) != withoutMeta(plain.Data) {
		t.Fatalf("round trip differs:\n--- before\n%s\n--- after\n%s", plain.Data, back.Data)
	}
	for name := range secrets {
		if got := vaultValue(t, b.vault, name); !secrets[name].Equal(got) {
			t.Fatalf("%s restored wrong", name)
		}
	}

	before := snapshot(t, b.st)
	again, err := b.svc.Apply(ctx, parse(t, full.Data), passphrase, "")
	if err != nil || again.Changed {
		t.Fatalf("re-import = %+v %v", again.Plan.Summary, err)
	}
	if after := snapshot(t, b.st); after != before {
		t.Fatal("a re-import wrote rows")
	}
	// The Homepage importer still recognises its own board on the restored hub.
	res2, secrets2, _ := homepage.Build(homepage.Input{Services: read("services.yaml"), Docker: read("docker.yaml"), Settings: read("settings.yaml")})
	if err := importer.New(b.st, b.vault).Diff(ctx, res2, secrets2); err != nil {
		t.Fatal(err)
	}
	for _, c := range res2.Plan.Changes {
		if c.Action != importer.ActionUnchanged {
			t.Fatalf("after a restore, a Homepage re-import would change %s %s: %s", c.Kind, c.ID, c.Action)
		}
	}
}
