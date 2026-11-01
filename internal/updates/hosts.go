package updates

import (
	"cmp"
	"encoding/json"
	"strconv"
	"strings"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/msg"
)

// Rows beyond images (docs/plan/14-v0.4.md, "One list"): a PVE host's
// pending apt packages, which the Proxmox integration reads only when its
// token has Sys.Modify (decision 2), and Unraid's OS version. Cha Dash
// updates neither: each row links to Proxmox's or Unraid's own page.

// MaxPackages is how many of a host's pending packages a row lists.
const MaxPackages = 100

// The labels an image's release notes come from (allowed into the graph
// by the Docker integration).
const (
	LabelSource  = "org.opencontainers.image.source"
	LabelVersion = "org.opencontainers.image.version"
)

// hostRows are the apt and OS rows the graph's hosts give.
func hostRows(doc graph.Doc) []Row {
	var out []Row
	for id, r := range doc.Resources {
		if r.Kind != "host" {
			continue
		}
		name := cmp.Or(r.Name, string(id))
		platform := stringAttr(r.Attrs, "platform")
		switch {
		case platform == "pve" && r.Attrs["updates"] != nil:
			row := Row{Resource: string(id), Name: name, Kind: KindApt, Host: name, State: integration.UpdateCurrent, Risk: RiskUnknown,
				Link: linkOf(r.Attrs, msg.New("updates.open_proxmox").Text())}
			n := intAttr(r.Attrs["updates"])
			row.Packages = packagesOf(r.Attrs["apt"])
			if n > 0 {
				row.State = integration.UpdateAvailable
				row.Latest = msg.New("updates.packages", "count", n).Text()
				row.Why = msg.New("updates.apt_elsewhere").Text()
				row.Risk = ""
				for _, p := range row.Packages {
					row.Risk = higherRisk(row.Risk, p.Risk)
				}
				if row.Risk == "" || len(row.Packages) < n {
					// Packages it didn't list could be anything.
					row.Risk = higherRisk(row.Risk, RiskUnknown)
				}
			}
			out = append(out, row)
		case platform == "unraid" && stringAttr(r.Attrs, "version") != "":
			out = append(out, Row{Resource: string(id), Name: name, Kind: KindOS, Host: name, Image: "Unraid",
				State: integration.UpdateCantCheck, Current: stringAttr(r.Attrs, "version"), Risk: RiskUnknown,
				Reason: msg.New("updates.unraid_cant_check").Text(),
				Link:   linkOf(r.Attrs, msg.New("updates.open_unraid").Text())})
		}
	}
	return out
}

// linkOf is the resource's first link (attrs.links), titled title.
func linkOf(attrs map[string]any, title string) *Link {
	var links []integration.Link
	if v, ok := attrs[integration.AttrLinks]; ok {
		b, _ := json.Marshal(v)
		_ = json.Unmarshal(b, &links)
	}
	for _, l := range links {
		if strings.HasPrefix(l.URL, "https://") || strings.HasPrefix(l.URL, "http://") {
			return &Link{Title: title, URL: l.URL}
		}
	}
	return nil
}

func intAttr(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

// packagesOf reads attrs.apt, [{"package","from","to"}], each with its
// risk.
func packagesOf(v any) []Package {
	if v == nil {
		return nil
	}
	var in []struct {
		Package string `json:"package"`
		From    string `json:"from"`
		To      string `json:"to"`
	}
	b, _ := json.Marshal(v)
	if json.Unmarshal(b, &in) != nil {
		return nil
	}
	out := make([]Package, 0, min(len(in), MaxPackages))
	for _, p := range in {
		if len(out) == MaxPackages {
			break
		}
		if p.Package == "" {
			continue
		}
		out = append(out, Package{Name: p.Package, From: p.From, To: p.To, Risk: Risk(p.From, p.To)})
	}
	return out
}

var riskRank = map[string]int{"": 0, RiskPatch: 1, RiskMinor: 2, RiskUnknown: 3, RiskMajor: 4}

// higherRisk is the riskier of two: major, then unknown, minor, patch.
func higherRisk(a, b string) string {
	if riskRank[b] > riskRank[a] {
		return b
	}
	return a
}

// Risk is what moving from one version to another risks, from their
// numbers only: major when the first number changes, minor the second,
// patch anything after it (a Debian revision included); unknown when
// either isn't a version or it doesn't move forward.
func Risk(from, to string) string {
	a, okA := parseVersion(from)
	b, okB := parseVersion(to)
	if !okA || !okB {
		return RiskUnknown
	}
	switch {
	case a.epoch != b.epoch:
		return RiskMajor
	case compareVersion(a, b) >= 0:
		return RiskUnknown
	case a.nums[0] != b.nums[0]:
		return RiskMajor
	case at(a.nums, 1) != at(b.nums, 1):
		return RiskMinor
	}
	return RiskPatch
}

// version is a version's leading numbers, its Debian epoch, and what
// follows them.
type version struct {
	epoch int
	nums  []int
	rest  string
}

// parseVersion reads "v4.0.1", "6.8.12-4", "1:2.36-9+deb12u7" or
// "4.0.15.2941-ls299": an optional epoch and v, then dotted numbers.
func parseVersion(s string) (version, bool) {
	var v version
	s = strings.TrimSpace(s)
	if e, rest, ok := strings.Cut(s, ":"); ok {
		n, err := strconv.Atoi(e)
		if err != nil {
			return v, false
		}
		v.epoch, s = n, rest
	}
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	i := 0
	for i < len(s) {
		j := i
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j == i {
			break
		}
		n, err := strconv.Atoi(s[i:j])
		if err != nil {
			return v, false
		}
		v.nums = append(v.nums, n)
		i = j
		if i < len(s) && s[i] == '.' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9' {
			i++
			continue
		}
		break
	}
	if len(v.nums) == 0 {
		return v, false
	}
	v.rest = s[i:]
	return v, true
}

func at(n []int, i int) int {
	if i < len(n) {
		return n[i]
	}
	return 0
}

// compareVersion orders two versions by epoch, numbers, then what follows
// (compared as text, so "-4" < "-5").
func compareVersion(a, b version) int {
	if a.epoch != b.epoch {
		return a.epoch - b.epoch
	}
	for i := range max(len(a.nums), len(b.nums)) {
		if d := at(a.nums, i) - at(b.nums, i); d != 0 {
			return d
		}
	}
	return strings.Compare(a.rest, b.rest)
}
