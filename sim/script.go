package sim

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

// Scenario names a scripted incident. Each scenario's behaviour (lead-up,
// phases, resource changes, metric shapes and copy) is implemented in Go; a
// Script decides which scenarios happen and when.
type Scenario string

// Scenarios of the Demo Home. They come from docs/plan/05-dogfood-home.md
// and from what the M0 spikes found in that lab.
const (
	ScenarioBackupFailed Scenario = "backup-failed"         // homeassistant's nightly backup fails
	ScenarioPBSFilling   Scenario = "pbs-datastore-filling" // PBS datastore crosses 90%
	ScenarioHostMemory   Scenario = "host-memory-pressure"  // jupiter-nas runs out of memory
	ScenarioPlexToken    Scenario = "plex-token-revoked"    // Plex answers 401 (a repair)
	ScenarioKarakeepAPI  Scenario = "karakeep-api-changed"  // Karakeep's stats endpoint 404s after an update (a repair)
	ScenarioQbitOOM      Scenario = "qbittorrent-oom"       // qBittorrent is OOM-killed and restarted

	ScenarioPlutoUnreachable Scenario = "pluto-backup-unreachable"  // pluto's nightly backup can't reach PBS
	ScenarioMarsUnreachable  Scenario = "mars-backup-unreachable"   // mars's nightly backup can't reach PBS
	ScenarioIOPassthrough    Scenario = "io-backup-passthrough"     // win-gaming's backup can't start: iolinux holds the GPU
	ScenarioGuestError       Scenario = "scratch-internal-error"    // jupiter-nas's VM scratch goes into internal-error
	ScenarioUnraidAPI        Scenario = "unraid-api-unsupported"    // Unraid's API is older than 4.37.4 (a repair)
	ScenarioHintTraefik      Scenario = "traefik-dashboard-no-auth" // a hint: Traefik's dashboard routed without auth
	ScenarioHintDocker       Scenario = "docker-proxy-broad"        // a hint: jupiter's socket proxy lets every container read through
	ScenarioHintFrigate      Scenario = "frigate-anonymous-admin"   // a hint: Frigate's internal port answers as an admin
)

// Offset is a duration after the simulation start. In JSON it is a Go
// duration string such as "14h3m".
type Offset time.Duration

// MarshalText encodes o as a duration string.
func (o Offset) MarshalText() ([]byte, error) { return []byte(time.Duration(o).String()), nil }

// UnmarshalText parses a duration string.
func (o *Offset) UnmarshalText(b []byte) error {
	d, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	*o = Offset(d)
	return nil
}

// ScriptEntry schedules one incident.
type ScriptEntry struct {
	Scenario Scenario `json:"scenario"`
	ID       string   `json:"id,omitempty"`  // defaults to the scenario name
	Open     Offset   `json:"open"`          // when the incident opens
	Fix      Offset   `json:"fix,omitempty"` // when the suggested action is applied; 0 = never (Sim.Fix can still apply it)
}

// Script is a list of scheduled incidents.
type Script []ScriptEntry

// DefaultAllOpen is the offset in the default script at which every incident
// is open at once (14:05 on the first day). incidents.json is taken there.
const DefaultAllOpen = 14*time.Hour + 5*time.Minute

// HintsOpen is when the default script's hints opened: when the instances
// first read the lab, nine days before the day starts. They stand all day.
const HintsOpen = -9*24*time.Hour + 2*time.Minute

// DefaultScript is the default day: the dogfood home's 2026-10-03 snapshot
// and what the spikes found there. The standing hints are open from the
// start; the incidents open through the night and morning, are all open at
// 14:05, and are fixed or clear one by one from the afternoon. The backups
// v0.1 can't fix clear with the next night's runs.
//
// IDs follow the hub's: <instance id>.<local id> for what the v0.1
// integrations raise, the scenario name for the original six.
func DefaultScript() Script {
	return Script{
		{Scenario: ScenarioHintTraefik, ID: "traefik.hint.api_no_auth", Open: Offset(HintsOpen)},
		{Scenario: ScenarioHintDocker, ID: "docker-jupiter.hint.docker_endpoint", Open: Offset(HintsOpen)},
		{Scenario: ScenarioHintFrigate, ID: "frigate.unauthenticated-admin", Open: Offset(HintsOpen)},
		{Scenario: ScenarioPlutoUnreachable, ID: "proxmox-pluto.backup.daily", Open: Offset(2 * time.Hour)},
		{Scenario: ScenarioBackupFailed, Open: Offset(2*time.Hour + 14*time.Minute), Fix: Offset(15*time.Hour + 30*time.Minute)},
		{Scenario: ScenarioMarsUnreachable, ID: "proxmox-mars.backup.daily", Open: Offset(2*time.Hour + 20*time.Minute)},
		{Scenario: ScenarioPBSFilling, Open: Offset(2*time.Hour + 44*time.Minute), Fix: Offset(16 * time.Hour)},
		{Scenario: ScenarioIOPassthrough, ID: "proxmox-io.backup.daily", Open: Offset(2*time.Hour + 55*time.Minute)},
		{Scenario: ScenarioKarakeepAPI, Open: Offset(9*time.Hour + 5*time.Minute), Fix: Offset(19 * time.Hour)},
		{Scenario: ScenarioGuestError, ID: "proxmox-jupiter-nas.guest.101", Open: Offset(11*time.Hour + 20*time.Minute), Fix: Offset(18*time.Hour + 5*time.Minute)},
		{Scenario: ScenarioUnraidAPI, ID: "unraid-jupiter.repair", Open: Offset(12*time.Hour + 5*time.Minute), Fix: Offset(16*time.Hour + 40*time.Minute)},
		{Scenario: ScenarioPlexToken, Open: Offset(13*time.Hour + 27*time.Minute), Fix: Offset(18 * time.Hour)},
		{Scenario: ScenarioHostMemory, Open: Offset(13*time.Hour + 30*time.Minute), Fix: Offset(17 * time.Hour)},
		{Scenario: ScenarioQbitOOM, Open: Offset(14*time.Hour + 3*time.Minute), Fix: Offset(20 * time.Hour)},
	}
}

// Validate checks that every scenario is known, IDs are unique, fixes come
// after opens, and two incidents of the same scenario don't overlap.
func (sc Script) Validate() error {
	seen := map[string]bool{}
	type span struct {
		id       string
		from, to time.Duration // to < 0: open-ended
	}
	byScenario := map[Scenario][]span{}
	for i, e := range sc {
		impl, ok := scenarios[e.Scenario]
		if !ok {
			return fmt.Errorf("sim: script entry %d: unknown scenario %q", i, e.Scenario)
		}
		id := e.ID
		if id == "" {
			id = string(e.Scenario)
		}
		if seen[id] {
			return fmt.Errorf("sim: script entry %d: duplicate incident id %q", i, id)
		}
		seen[id] = true
		if e.Fix != 0 && e.Fix <= e.Open {
			return fmt.Errorf("sim: incident %q: fix (%v) must come after open (%v)", id, time.Duration(e.Fix), time.Duration(e.Open))
		}
		byScenario[e.Scenario] = append(byScenario[e.Scenario], span{id, time.Duration(e.Open) - impl.lead(), impl.horizonOffset(time.Duration(e.Open), time.Duration(e.Fix))})
	}
	for s, spans := range byScenario {
		slices.SortFunc(spans, func(a, b span) int { return int(a.from - b.from) })
		for i := 1; i < len(spans); i++ {
			prev := spans[i-1]
			if prev.to < 0 || spans[i].from <= prev.to {
				return fmt.Errorf("sim: incidents %q and %q of scenario %s overlap", prev.id, spans[i].id, s)
			}
		}
	}
	return nil
}

// ErrUnknownIncident is returned by Sim.Fix for an ID not in the script.
var ErrUnknownIncident = errors.New("sim: unknown incident")

// Why Sim.Fix (and CanFix) refuse an incident in the script.
var (
	ErrNotOpen  = errors.New("sim: not open yet")
	ErrResolved = errors.New("sim: already resolved")
	ErrFixing   = errors.New("sim: already being fixed")
)

// instance is one scheduled incident.
type instance struct {
	id   string
	sc   Scenario
	impl scenario
	open time.Time
	fix  time.Time // zero: not fixed (yet)
}

func (in *instance) fixed(t time.Time) bool { return !in.fix.IsZero() && !t.Before(in.fix) }

// scenario is the behaviour of one Scenario. Every method is a pure function
// of the instance and the time, which keeps the simulation deterministic.
type scenario interface {
	// lead is how long before Open the scenario starts shaping metrics.
	lead() time.Duration
	// horizon is when the incident resolves; zero if it never does on its own.
	horizon(in *instance) time.Time
	// horizonOffset is horizon for script validation (fix 0 = none); < 0 means open-ended.
	horizonOffset(open, fix time.Duration) time.Duration
	// changePoints lists every moment the scenario changes a resource or the incident.
	changePoints(in *instance) []time.Time
	// apply changes resources in w (at w.t).
	apply(s *Sim, in *instance, w *world)
	// incident returns the incident as of t, or nil before it opens.
	incident(s *Sim, in *instance, t time.Time) *Incident
	// touches reports whether the scenario shapes a metric.
	touches(res, key string) bool
	// adjust reshapes a metric value.
	adjust(s *Sim, in *instance, m *metricSpec, t time.Time, v float64) float64
}

var scenarios = map[Scenario]scenario{
	ScenarioBackupFailed:     backupFailed{},
	ScenarioPBSFilling:       pbsFilling{},
	ScenarioHostMemory:       hostMemory{},
	ScenarioPlexToken:        plexToken{},
	ScenarioKarakeepAPI:      karakeepAPI{},
	ScenarioQbitOOM:          qbitOOM{},
	ScenarioPlutoUnreachable: backupUnreachable{job: "pve:pluto/backup/daily"},
	ScenarioMarsUnreachable:  backupUnreachable{job: "pve:mars/backup/daily"},
	ScenarioIOPassthrough:    backupPassthrough{},
	ScenarioGuestError:       guestError{},
	ScenarioUnraidAPI:        unraidAPI{},
	ScenarioHintTraefik:      hintTraefik,
	ScenarioHintDocker:       hintDocker,
	ScenarioHintFrigate:      hintFrigate,
}
