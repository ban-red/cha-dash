package arr

import (
	"context"
	"net/url"
	"time"

	"dash.cha.sh/internal/integration"
)

// SlowEvery is how often the expensive reads (status, wanted counts, the
// calendar) run while someone watches.
const SlowEvery = 10 * time.Minute

// MediaApp is what differs between Sonarr, Radarr and Lidarr: everything else about
// them is the same instance.
type MediaApp struct {
	App
	MissingKey string     // missing.episodes, missing.movies
	MissingMax float64    // its scale
	QueueBand  [2]float64 // the normal range of queue (the Demo Home's)
	WantedBand [2]float64 // and of wanted and the missing count
	// Calendar reads the upcoming releases in now's window (CalendarQuery).
	Calendar func(ctx context.Context, c *Client, now time.Time) ([]CalendarItem, error)
}

// Poll is a media *arr's polling plan: every minute while watched, every 5
// while not (health and the queue only), the slow reads every SlowEvery.
var Poll = integration.Poll{Interval: time.Minute, Idle: 5 * time.Minute, Timeout: 30 * time.Second}

// NewMedia returns the instance for a Sonarr, Radarr or Lidarr config. Connect calls
// it; it does no I/O.
func NewMedia(m MediaApp, cfg integration.Config, d integration.Deps) integration.Instance {
	return &media{m: m, c: New(m.App, cfg, d), cfg: cfg, d: d, slow: Every{D: SlowEvery}}
}

type media struct {
	m    MediaApp
	c    *Client
	cfg  integration.Config
	d    integration.Deps
	slow Every

	// From the last slow read.
	version         string
	missing, cutoff int
	haveMissing     bool
	haveCutoff      bool
	calendar        []CalendarItem
}

func (in *media) Test(ctx context.Context) integration.TestResult { return in.c.Test(ctx) }

func (in *media) Close() error { return nil }

func (in *media) Sync(ctx context.Context, want integration.Interest) error {
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
	q, err := in.c.Queue(ctx)
	if err != nil {
		return err
	}
	h := HealthOf(in.m.App, health)
	if h.Status == integration.HealthOK && (q.Errors || q.UnknownErrors) {
		h = integration.Health{Status: integration.HealthAttention, Reason: "A download in " + in.m.Name + "'s queue has an error"}
	}
	res := in.c.AppResource(in.cfg, in.version, h, "media.calendar")
	res.Attrs["calendar"] = Calendar(in.calendar, now)
	wanted := integration.Unknown()
	if in.haveMissing {
		wanted = float64(in.missing)
		if in.haveCutoff {
			wanted += float64(in.cutoff)
		}
	}
	return in.d.Emit.Replace("", integration.Facts{
		Resources: []integration.Resource{res},
		Relations: []integration.Relation{{From: in.d.Emit.Self(), Type: "monitors", To: res.ID}},
		Metrics: []integration.Metric{
			Count(res.ID, "queue", 200, in.m.QueueBand, float64(q.TotalCount)),
			Count(res.ID, "wanted", 5000, in.m.WantedBand, wanted),
			Count(res.ID, in.m.MissingKey, in.m.MissingMax, in.m.WantedBand, Value(in.missing, in.haveMissing)),
		},
	})
}

// readSlow refreshes the version, the wanted counts and the calendar. The
// wanted endpoints are optional (Radarr has them from v5): without them the
// counts are unknown.
func (in *media) readSlow(ctx context.Context, now time.Time) error {
	st, err := in.c.Status(ctx)
	if err != nil {
		return err
	}
	in.version = st.Version
	in.d.Emit.SetVersion(st.Version)
	monitored := url.Values{"monitored": {"true"}}
	if in.missing, in.haveMissing, err = in.c.Total(ctx, "/wanted/missing", monitored); err != nil {
		return err
	}
	if in.cutoff, in.haveCutoff, err = in.c.Total(ctx, "/wanted/cutoff", monitored); err != nil {
		return err
	}
	cal, err := in.m.Calendar(ctx, in.c, now)
	if err != nil {
		return err
	}
	in.calendar = cal
	return nil
}
