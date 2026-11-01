package arr

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"dash.cha.sh/internal/integration"
)

// provider is a download client, an indexer or (Prowlarr) an application,
// as the *arrs list them: a name and a list of settings fields. The fields
// also hold usernames, passwords and API keys; only the address is read.
type provider struct {
	Name           string  `json:"name"`
	Implementation string  `json:"implementation"`
	Enable         *bool   `json:"enable"`    // download clients
	EnableRss      *bool   `json:"enableRss"` // indexers
	EnableSearch   *bool   `json:"enableAutomaticSearch"`
	SyncLevel      string  `json:"syncLevel"` // Prowlarr's applications: "disabled" when off
	Fields         []field `json:"fields"`
}

type field struct {
	Name  string `json:"name"`
	Value any    `json:"value"`
}

func (p provider) field(name string) any {
	for _, f := range p.Fields {
		if strings.EqualFold(f.Name, name) {
			return f.Value
		}
	}
	return nil
}

func (p provider) off() bool {
	switch {
	case p.Enable != nil:
		return !*p.Enable
	case p.EnableRss != nil || p.EnableSearch != nil:
		return (p.EnableRss == nil || !*p.EnableRss) && (p.EnableSearch == nil || !*p.EnableSearch)
	}
	return strings.EqualFold(p.SyncLevel, "disabled")
}

// address is the provider's URL, from a baseUrl field (indexers, Prowlarr's
// applications) or from host, port, useSsl and urlBase (download clients).
// "" when it has none (a blackhole client). Without path, a baseUrl keeps
// only scheme, host and port: an indexer's can be a whole feed URL, and
// private trackers put the passkey in its path.
func (p provider) address(path bool) string {
	if s, _ := p.field("baseUrl").(string); s != "" {
		u, err := url.Parse(strings.TrimSpace(s))
		if err != nil {
			return ""
		}
		port, _ := strconv.Atoi(u.Port())
		if !path {
			u.Path = ""
		}
		return integration.UseURL(u.Scheme, u.Hostname(), port, u.Path)
	}
	host, _ := p.field("host").(string)
	if host == "" {
		return ""
	}
	scheme := "http"
	if tls, _ := p.field("useSsl").(bool); tls {
		scheme = "https"
	}
	var port int
	switch v := p.field("port").(type) {
	case float64:
		port = int(v)
	case string:
		port, _ = strconv.Atoi(v)
	}
	base, _ := p.field("urlBase").(string)
	return integration.UseURL(scheme, host, port, base)
}

// UsesFrom reads lists of providers, one role each ("/downloadclient":
// "download client"), into the app's uses: the enabled ones with an
// address, by name. A list the version lacks is skipped.
func (c *Client) UsesFrom(ctx context.Context, lists ...[2]string) ([]integration.Use, error) {
	var out []integration.Use
	for _, l := range lists {
		var ps []provider
		if _, err := c.GetOptional(ctx, l[0], nil, &ps); err != nil {
			return nil, err
		}
		for _, p := range ps {
			if p.off() {
				continue
			}
			if u := p.address(l[1] != RoleIndexer); u != "" {
				out = append(out, integration.Use{Role: l[1], URL: u, Name: strings.TrimSpace(p.Name)})
			}
		}
	}
	return out, nil
}

// MediaUses are what Sonarr, Radarr and Lidarr use: download clients and
// indexers.
var MediaUses = [][2]string{{"/downloadclient", "download client"}, {"/indexer", RoleIndexer}}

// RoleIndexer is an indexer's role. Its URL keeps no path (see address).
const RoleIndexer = "indexer"
