// Package prowlarr reads Prowlarr (API v1): indexer health (which enabled
// indexers are failing), grabs, the last day's queries and failures, and
// health checks. See README.md.
package prowlarr

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"dash.cha.sh/integrations/internal/arr"
	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "prowlarr"

var app = arr.App{ID: ID, Name: "Prowlarr", APIPath: "/api/v1", MinMajor: 1}

func init() { integration.Register(Integration{}) }

// Integration is the Prowlarr integration.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	return integration.Manifest{
		ID: ID, Name: "Prowlarr", Icon: "prowlarr", Version: "0.1.0", Tier: integration.Silver,
		Category:     integration.CatAutomation,
		Capabilities: []integration.Capability{"downloads.indexers"},
		ConfigSchema: arr.Schema(app),
		Poll:         arr.Poll,
	}
}

// Connect implements integration.Integration.
func (Integration) Connect(_ context.Context, cfg integration.Config, d integration.Deps) (integration.Instance, error) {
	return &instance{c: arr.New(app, cfg, d), cfg: cfg, d: d, slow: arr.Every{D: arr.SlowEvery}}, nil
}

type instance struct {
	c    *arr.Client
	cfg  integration.Config
	d    integration.Deps
	slow arr.Every

	// From the last slow read.
	version            string
	grabs              int
	queries, failures  int
	haveStats, haveDay bool
}

// Test implements integration.Instance.
func (in *instance) Test(ctx context.Context) integration.TestResult { return in.c.Test(ctx) }

// Close implements integration.Instance.
func (in *instance) Close() error { return nil }

// indexer is the part of IndexerResource used here.
type indexer struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Enable   bool   `json:"enable"`
	Protocol string `json:"protocol"`
}

// indexerStatus is an IndexerStatusResource: Prowlarr lists the indexers
// that failed recently, and until when it has disabled them.
type indexerStatus struct {
	IndexerID         int        `json:"indexerId"`
	DisabledTill      *time.Time `json:"disabledTill"`
	MostRecentFailure *time.Time `json:"mostRecentFailure"`
	InitialFailure    *time.Time `json:"initialFailure"`
}

// stats is IndexerStatsResource, per indexer.
type stats struct {
	Indexers []struct {
		IndexerID                 int `json:"indexerId"`
		NumberOfQueries           int `json:"numberOfQueries"`
		NumberOfGrabs             int `json:"numberOfGrabs"`
		NumberOfRssQueries        int `json:"numberOfRssQueries"`
		NumberOfAuthQueries       int `json:"numberOfAuthQueries"`
		NumberOfFailedQueries     int `json:"numberOfFailedQueries"`
		NumberOfFailedGrabs       int `json:"numberOfFailedGrabs"`
		NumberOfFailedRssQueries  int `json:"numberOfFailedRssQueries"`
		NumberOfFailedAuthQueries int `json:"numberOfFailedAuthQueries"`
	} `json:"indexers"`
}

// Sync implements integration.Instance.
func (in *instance) Sync(ctx context.Context, want integration.Interest) error {
	now := in.d.Now()
	if in.slow.Due(now, want.Watched) {
		if err := in.readSlow(ctx, now); err != nil {
			return err
		}
		in.slow.Done(now)
	}
	health, err := in.c.Health(ctx)
	if err != nil {
		return err
	}
	var idx []indexer
	if err := in.c.Get(ctx, "/indexer", nil, &idx); err != nil {
		return err
	}
	var sts []indexerStatus
	if err := in.c.Get(ctx, "/indexerstatus", nil, &sts); err != nil {
		return err
	}
	enabled, failing := failingIndexers(idx, sts, now)

	h := arr.HealthOf(app, health)
	if h.Status == integration.HealthOK && len(failing) > 0 {
		h = integration.Health{Status: integration.HealthAttention, Reason: failingReason(failing, enabled)}
	}
	res := in.c.AppResource(in.cfg, in.version, h, "downloads.indexers")
	res.Attrs["indexers"] = enabled
	if len(failing) > 0 {
		res.Attrs["indexers_failing"] = failing
	}
	failMetric := arr.Count(res.ID, "indexers.failing", 1000, [2]float64{0, 0}, float64(len(failing)))
	failMetric.Warn = 1
	return in.d.Emit.Replace("", integration.Facts{
		Resources: []integration.Resource{res},
		Relations: []integration.Relation{{From: in.d.Emit.Self(), Type: "monitors", To: res.ID}},
		Metrics: []integration.Metric{
			arr.Count(res.ID, "grabs", 1e7, [2]float64{0, 2000}, arr.Value(in.grabs, in.haveStats)),
			arr.Count(res.ID, "queries.24h", 1e5, [2]float64{20, 80}, arr.Value(in.queries, in.haveDay)),
			arr.Count(res.ID, "failures.24h", 1e4, [2]float64{0, 2}, arr.Value(in.failures, in.haveDay)),
			arr.Count(res.ID, "indexers", 1000, [2]float64{0, 1000}, float64(enabled)),
			failMetric,
		},
	})
}

// failingIndexers counts the enabled indexers and names the enabled ones
// Prowlarr reports failing (a status entry: disabled for now, or a recent
// failure it's backing off from), sorted.
func failingIndexers(idx []indexer, sts []indexerStatus, now time.Time) (enabled int, failing []string) {
	bad := map[int]bool{}
	for _, s := range sts {
		if (s.DisabledTill != nil && s.DisabledTill.After(now)) || s.MostRecentFailure != nil {
			bad[s.IndexerID] = true
		}
	}
	for _, i := range idx {
		if !i.Enable {
			continue
		}
		enabled++
		if bad[i.ID] {
			failing = append(failing, i.Name)
		}
	}
	slices.Sort(failing)
	return enabled, failing
}

func failingReason(failing []string, enabled int) string {
	names := failing
	more := ""
	if len(names) > 3 {
		names, more = names[:3], fmt.Sprintf(" and %d more", len(failing)-3)
	}
	verb := "is"
	if len(failing) > 1 {
		verb = "are"
	}
	return fmt.Sprintf("%s%s %s failing (%d of %d indexers)", strings.Join(names, ", "), more, verb, len(failing), enabled)
}

// readSlow refreshes the version, the all-time grabs and the last day's
// queries and failures.
func (in *instance) readSlow(ctx context.Context, now time.Time) error {
	st, err := in.c.Status(ctx)
	if err != nil {
		return err
	}
	in.version = st.Version
	in.d.Emit.SetVersion(st.Version)

	var all stats
	if in.haveStats, err = in.c.GetOptional(ctx, "/indexerstats", nil, &all); err != nil {
		return err
	}
	in.grabs = 0
	for _, s := range all.Indexers {
		in.grabs += s.NumberOfGrabs
	}
	var day stats
	q := url.Values{
		"startDate": {now.UTC().Add(-24 * time.Hour).Format(time.RFC3339)},
		"endDate":   {now.UTC().Format(time.RFC3339)},
	}
	if in.haveDay, err = in.c.GetOptional(ctx, "/indexerstats", q, &day); err != nil {
		return err
	}
	in.queries, in.failures = 0, 0
	for _, s := range day.Indexers {
		in.queries += s.NumberOfQueries + s.NumberOfRssQueries
		in.failures += s.NumberOfFailedQueries + s.NumberOfFailedRssQueries + s.NumberOfFailedGrabs + s.NumberOfFailedAuthQueries
	}
	return nil
}
