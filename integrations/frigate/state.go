package frigate

import (
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"dash.cha.sh/internal/integration"
)

// instance is one Frigate. Sync and Subscribe run concurrently and share
// everything below mu.
type instance struct {
	d      integration.Deps
	base   *url.URL
	http   *http.Client
	name   string
	link   string // the configured URL, for the app's link
	prefix string // "frigate:<instance id>"
	appID  string
	creds  bool

	mu           sync.Mutex
	synced       bool // the first sync has published
	needFull     bool // read everything on the next sync (first, or after a failure)
	version      string
	latest       string
	startedAt    time.Time
	detector     string
	statsEvery   time.Duration
	haveConfig   bool
	configAt     time.Time
	configStale  bool
	cams         map[string]*camera
	inference    float64
	statsAt      time.Time
	statsFromWS  bool
	eventsAt     time.Time
	eventsDirty  bool
	countsCapped bool
	wsLive       bool
	anonymous    bool
	allowed      []string // profile.allowed_cameras, when the role is limited
}

// camera is what's known about one camera, from config, stats, the
// WebSocket and events.
type camera struct {
	key       string // Frigate's camera name, the end of the resource ID
	name      string
	order     int
	inConfig  bool
	enabled   bool
	detect    bool
	record    bool
	fps       float64 // configured detect fps
	host      string  // the camera's address, from its stream URL
	haveStats bool
	cameraFPS float64
	expected  float64
	quality   string
	reconnect int
	stalls    int
	wsStatus  string // online, offline, disabled; "" when the WebSocket isn't live
	motion    *bool  // WebSocket only
	count     int    // tracked objects in the last 24 h
	lastEvent event
	lastHuman event
}

// event is one tracked object, as /api/events and the WebSocket's events
// topic report it. sub_label (a recognised face or plate) is never read.
type event struct {
	Camera        string   `json:"camera"`
	Label         string   `json:"label"`
	StartTime     float64  `json:"start_time"`
	TopScore      *float64 `json:"top_score"`
	Score         *float64 `json:"score"`
	FalsePositive *bool    `json:"false_positive"`
	Data          struct {
		TopScore *float64 `json:"top_score"`
		Score    *float64 `json:"score"`
	} `json:"data"`
}

func (e event) at() time.Time {
	sec, frac := math.Modf(e.StartTime)
	return time.Unix(int64(sec), int64(frac*1e9)).UTC()
}

func (e event) score() float64 {
	for _, p := range []*float64{e.TopScore, e.Data.TopScore, e.Score, e.Data.Score} {
		if p != nil && *p > 0 {
			return math.Round(*p*100) / 100
		}
	}
	return 0
}

// frigateConfig is the part of /api/config the integration reads. The rest
// (stream URLs, MQTT identity, …) is sensitive and never kept.
type frigateConfig struct {
	Cameras map[string]struct {
		Enabled      *bool  `json:"enabled"`
		FriendlyName string `json:"friendly_name"`
		Detect       struct {
			Enabled *bool   `json:"enabled"`
			FPS     float64 `json:"fps"`
		} `json:"detect"`
		Record struct {
			Enabled *bool `json:"enabled"`
		} `json:"record"`
		FFmpeg struct {
			Inputs []ffmpegInput `json:"inputs"`
		} `json:"ffmpeg"`
		UI struct {
			Order int `json:"order"`
		} `json:"ui"`
	} `json:"cameras"`
	Detectors map[string]struct {
		Type string `json:"type"`
	} `json:"detectors"`
	MQTT struct {
		StatsInterval float64 `json:"stats_interval"`
	} `json:"mqtt"`
	Go2RTC struct {
		Streams map[string]json.RawMessage `json:"streams"`
	} `json:"go2rtc"`
}

// ffmpegInput is one of a camera's streams. Only its host is ever kept.
type ffmpegInput struct {
	Path  string   `json:"path"`
	Roles []string `json:"roles"`
}

// stats is /api/stats, and the WebSocket's stats topic.
type stats struct {
	Cameras map[string]struct {
		CameraFPS         float64  `json:"camera_fps"`
		ExpectedFPS       *float64 `json:"expected_fps"`
		ConnectionQuality string   `json:"connection_quality"`
		Reconnects        int      `json:"reconnects_last_hour"`
		Stalls            int      `json:"stalls_last_hour"`
		DetectionEnabled  *bool    `json:"detection_enabled"`
	} `json:"cameras"`
	Detectors map[string]struct {
		InferenceSpeed float64 `json:"inference_speed"`
	} `json:"detectors"`
	Service struct {
		Version       string  `json:"version"`
		LatestVersion string  `json:"latest_version"`
		Uptime        float64 `json:"uptime"`
		LastUpdated   float64 `json:"last_updated"`
	} `json:"service"`
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// humanize turns a camera key into a display name: "side_deck" → "Side deck".
func humanize(key string) string {
	s := strings.TrimSpace(strings.NewReplacer("_", " ", "-", " ").Replace(key))
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return key
	}
	return string(unicode.ToUpper(r)) + s[n:]
}

// cam returns the camera named key, creating it when the config hasn't been
// read yet (stats or the WebSocket name it first).
func (in *instance) cam(key string, create bool) *camera {
	if c, ok := in.cams[key]; ok {
		return c
	}
	if !create || in.haveConfig || key == "" || strings.Contains(key, "/") {
		return nil
	}
	c := &camera{key: key, name: humanize(key), enabled: true, detect: true}
	in.cams[key] = c
	return c
}

// applyConfig replaces the camera set with the config's, keeping what
// stats, the WebSocket and events said about cameras that stay.
func (in *instance) applyConfig(cfg frigateConfig, now time.Time) {
	next := map[string]*camera{}
	for key, cc := range cfg.Cameras {
		if len(in.allowed) > 0 && !slices.Contains(in.allowed, key) {
			continue
		}
		c := in.cams[key]
		if c == nil {
			c = &camera{key: key}
		}
		c.inConfig = true
		c.name = cc.FriendlyName
		if c.name == "" {
			c.name = humanize(key)
		}
		c.order = cc.UI.Order
		c.enabled = boolOr(cc.Enabled, true)
		c.detect = boolOr(cc.Detect.Enabled, true)
		c.record = boolOr(cc.Record.Enabled, false)
		c.fps = cc.Detect.FPS
		c.host = ""
		// The detect input first: it's the stream Frigate watches.
		inputs := slices.Clone(cc.FFmpeg.Inputs)
		slices.SortStableFunc(inputs, func(a, b ffmpegInput) int {
			ad, bd := slices.Contains(a.Roles, "detect"), slices.Contains(b.Roles, "detect")
			switch {
			case ad && !bd:
				return -1
			case bd && !ad:
				return 1
			}
			return 0
		})
		for _, input := range inputs {
			if c.host = streamHost(input.Path, cfg.Go2RTC.Streams); c.host != "" {
				break
			}
		}
		next[key] = c
	}
	in.cams = next
	in.haveConfig = true
	in.configAt, in.configStale = now, false
	in.detector = ""
	names := make([]string, 0, len(cfg.Detectors))
	for name := range cfg.Detectors {
		names = append(names, name)
	}
	slices.Sort(names)
	var kinds []string
	for _, n := range names {
		if t := cfg.Detectors[n].Type; t != "" && !slices.Contains(kinds, t) {
			kinds = append(kinds, t)
		}
	}
	in.detector = strings.Join(kinds, ", ")
	in.statsEvery = defaultStatsEvery
	if cfg.MQTT.StatsInterval > 0 {
		in.statsEvery = time.Duration(cfg.MQTT.StatsInterval * float64(time.Second))
	}
}

// streamHost resolves a camera input to the camera's address. Inputs often
// point at go2rtc's restream on localhost (rtsp://127.0.0.1:8554/<stream>),
// so those are looked up in go2rtc's streams.
func streamHost(path string, streams map[string]json.RawMessage) string {
	if h := hostOf(path); h != "" {
		return h
	}
	u, err := url.Parse(path)
	if err != nil || u.Host == "" {
		return ""
	}
	name := strings.Trim(u.Path, "/")
	raw, ok := streams[name]
	if !ok {
		return ""
	}
	var srcs []string
	if json.Unmarshal(raw, &srcs) != nil {
		var one string
		if json.Unmarshal(raw, &one) != nil {
			return ""
		}
		srcs = []string{one}
	}
	for _, s := range srcs {
		if h := hostOf(s); h != "" {
			return h
		}
	}
	return ""
}

// applyStats folds a stats document (polled or pushed) into the state.
func (in *instance) applyStats(st stats, now time.Time, fromWS bool) {
	for key, cs := range st.Cameras {
		c := in.cam(key, true)
		if c == nil {
			continue
		}
		c.haveStats = true
		c.cameraFPS = cs.CameraFPS
		c.expected = c.fps
		if cs.ExpectedFPS != nil {
			c.expected = *cs.ExpectedFPS
		}
		c.quality = cs.ConnectionQuality
		c.reconnect, c.stalls = cs.Reconnects, cs.Stalls
		if cs.DetectionEnabled != nil {
			c.detect = *cs.DetectionEnabled
		}
	}
	var sum float64
	var n int
	for _, d := range st.Detectors {
		if d.InferenceSpeed > 0 {
			sum += d.InferenceSpeed
			n++
		}
	}
	in.inference = integration.Unknown()
	if n > 0 {
		in.inference = math.Round(sum/float64(n)*100) / 100
	}
	if v := st.Service.LatestVersion; v != "" && !strings.EqualFold(v, "disabled") {
		in.latest = v
	}
	if st.Service.Uptime > 0 && st.Service.LastUpdated > 0 {
		started := time.Unix(int64(st.Service.LastUpdated-st.Service.Uptime), 0).UTC()
		// Uptime and last_updated are sampled together but not atomically:
		// only move started_at when it really changed (a restart).
		if in.startedAt.IsZero() || started.Sub(in.startedAt).Abs() > time.Minute {
			in.startedAt = started
		}
	}
	in.statsAt, in.statsFromWS = now, fromWS
}

// applyEvents recounts tracked objects per camera over the 24 h before now.
func (in *instance) applyEvents(evs []event, now time.Time) {
	from := now.Add(-24 * time.Hour)
	for _, c := range in.cams {
		c.count = 0
	}
	for _, e := range evs {
		c := in.cams[e.Camera]
		if c == nil || boolOr(e.FalsePositive, false) {
			continue
		}
		if e.at().After(from) {
			c.count++
		}
		c.noteEvent(e)
	}
	in.countsCapped = len(evs) >= eventsLimit
	in.eventsAt, in.eventsDirty = now, false
}

// noteEvent keeps the newest event and the newest person per camera.
func (c *camera) noteEvent(e event) {
	if e.at().After(c.lastEvent.at()) || c.lastEvent.StartTime == 0 {
		c.lastEvent = e
	}
	if e.Label == "person" && (e.at().After(c.lastHuman.at()) || c.lastHuman.StartTime == 0) {
		c.lastHuman = e
	}
}

// health is a camera's state and health, freshest source first.
func (c *camera) health() (string, integration.Health) {
	switch {
	case !c.enabled || c.wsStatus == "disabled":
		return "stopped", integration.Health{Status: integration.HealthOK, Reason: "Turned off in Frigate"}
	case c.wsStatus == "offline":
		return "error", integration.Health{Status: integration.HealthDown, Reason: "Frigate reports the stream offline and is restarting it"}
	case c.wsStatus == "" && c.haveStats && c.cameraFPS <= 0:
		return "error", integration.Health{Status: integration.HealthDown, Reason: "No frames from the camera"}
	case !c.haveStats && c.wsStatus == "":
		return "unknown", integration.Health{Status: integration.HealthUnknown}
	}
	var issues []string
	if q := strings.ToLower(c.quality); q != "" && q != "excellent" && q != "good" {
		issues = append(issues, "connection quality is "+q)
	}
	if c.reconnect > 0 {
		issues = append(issues, fmt.Sprintf("reconnected %d %s in the last hour", c.reconnect, plural(c.reconnect, "time")))
	}
	if c.haveStats && c.expected > 0 && c.cameraFPS > 0 && c.cameraFPS < c.expected/2 {
		issues = append(issues, fmt.Sprintf("%.1f fps of %g expected", c.cameraFPS, c.expected))
	}
	if len(issues) > 0 {
		reason := strings.Join(issues, "; ")
		return "online", integration.Health{Status: integration.HealthAttention, Reason: strings.ToUpper(reason[:1]) + reason[1:]}
	}
	return "online", integration.Health{Status: integration.HealthOK}
}

func plural(n int, w string) string {
	if n == 1 {
		return w
	}
	return w + "s"
}

func (in *instance) camID(key string) string { return in.prefix + "/camera/" + key }

func (in *instance) sortedCams() []*camera {
	out := make([]*camera, 0, len(in.cams))
	for _, c := range in.cams {
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b *camera) int {
		if a.order != b.order {
			return a.order - b.order
		}
		return strings.Compare(a.key, b.key)
	})
	return out
}

// graphFacts are the resources and relations: the app and its cameras.
func (in *instance) graphFacts() integration.Facts {
	self := in.d.Emit.Self()
	attrs := map[string]any{"type": ID, "version": semver(in.version), "cameras": len(in.cams), "url": in.link}
	if in.latest != "" && in.latest != semver(in.version) {
		attrs["latest_version"] = in.latest
	}
	if in.detector != "" {
		attrs["detector"] = in.detector
	}
	if !in.startedAt.IsZero() {
		attrs["started_at"] = in.startedAt.Format(time.RFC3339)
	}
	app := integration.Resource{
		ID: in.appID, Kind: "app", Name: in.name, State: "running", Desired: "running",
		Health: integration.Health{Status: integration.HealthOK}, Attrs: attrs,
		Links: []integration.Link{{Title: in.name, URL: in.link}},
		Caps:  []string{"cameras", "cameras.events"},
	}
	f := integration.Facts{Resources: []integration.Resource{app}}
	f.Relations = append(f.Relations, integration.Relation{From: self, Type: "monitors", To: in.appID})
	for _, c := range in.sortedCams() {
		id := in.camID(c.key)
		state, health := c.health()
		a := map[string]any{"camera": c.key, "detect": c.detect, "record": c.record}
		if c.fps > 0 {
			a["fps"] = c.fps
		}
		if c.host != "" {
			if net.ParseIP(c.host) != nil {
				a["ip"] = c.host
			} else {
				a["host"] = c.host
			}
		}
		if c.motion != nil {
			a["motion"] = *c.motion
		}
		if e := c.lastEvent; e.StartTime > 0 {
			a["last_event.at"] = e.at().Truncate(time.Second).Format(time.RFC3339)
			a["last_event.label"] = e.Label
			if s := e.score(); s > 0 {
				a["last_event.score"] = s
			}
		}
		if e := c.lastHuman; e.StartTime > 0 {
			a["last_person.at"] = e.at().Truncate(time.Second).Format(time.RFC3339)
			if s := e.score(); s > 0 {
				a["last_person.score"] = s
			}
		}
		f.Resources = append(f.Resources, integration.Resource{
			ID: id, Kind: "camera", Name: c.name, State: state, Health: health, Attrs: a,
			Caps: []string{"cameras"},
		})
		f.Relations = append(f.Relations,
			integration.Relation{From: in.appID, Type: "exposes", To: id},
			integration.Relation{From: self, Type: "monitors", To: id})
	}
	return f
}

// statsFacts is the detector's inference time, the simulator's scale.
func (in *instance) statsFacts() integration.Facts {
	return integration.Facts{Metrics: []integration.Metric{{
		Resource: in.appID, Key: "inference.ms", Unit: "ms", Min: 1, Max: 500, Band: [2]float64{5, 20}, Value: in.inference,
	}}}
}

// eventsFacts is the tracked objects per camera over the last 24 h.
func (in *instance) eventsFacts() integration.Facts {
	var f integration.Facts
	for _, c := range in.sortedCams() {
		f.Metrics = append(f.Metrics, integration.Metric{
			Resource: in.camID(c.key), Key: "detections.24h", Unit: "count", Min: 0, Max: 500, Band: [2]float64{0, 40}, Value: float64(c.count),
		})
	}
	return f
}
