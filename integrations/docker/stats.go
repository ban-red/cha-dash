package docker

import (
	"context"
	"math"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// Container stats: cpu.pct and mem.mib for the containers someone watches
// closely (a sheet open on each: integration.Interest.Resources), never for
// every container on every tick.
const (
	// statsInterval is the sync interval while a container is watched.
	statsInterval = 10 * time.Second
	// maxWatched bounds the stats requests in one sync.
	maxWatched = 16
	// statsScope is the Emit scope the series live in, apart from the
	// engine's resources, so they come and go with the interest.
	statsScope = "stats"
)

// stats is the part of Docker's stats answer the integration reads.
type stats struct {
	CPU struct {
		Usage struct {
			Total uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		System uint64 `json:"system_cpu_usage"`
	} `json:"cpu_stats"`
	Memory struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
}

// cpuSample is the previous reading a container's CPU share is measured
// against.
type cpuSample struct{ total, system uint64 }

// IntervalFor implements integration.InterestIntervaler: every
// statsInterval while one of this engine's containers is watched and the
// endpoint allows stats, else Interval.
func (in *instance) IntervalFor(want integration.Interest) time.Duration {
	if in.statsAllowed() && len(in.watched(want)) > 0 {
		return statsInterval
	}
	return in.Interval()
}

func (in *instance) statsAllowed() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.acc.stats
}

// watched lists the names of this engine's containers in want.Resources.
func (in *instance) watched(want integration.Interest) []string {
	prefix := in.rid("container/")
	var names []string
	for _, id := range want.Resources {
		if name, ok := strings.CutPrefix(id, prefix); ok && name != "" && len(names) < maxWatched {
			names = append(names, name)
		}
	}
	return names
}

// refreshDue reports whether the engine's list is due again while a
// container is watched: syncs then come every statsInterval, but the list
// keeps its own pace.
func (in *instance) refreshDue() bool {
	return in.listed == nil || in.d.Now().Sub(in.refreshedAt) >= in.Interval()-statsInterval/2
}

// publishStats reads the watched running containers' stats and publishes
// them, or clears the series when nobody watches. A container whose stats
// fail shows as unknown; stats never fail the sync.
func (in *instance) publishStats(ctx context.Context, want integration.Interest) error {
	names := in.watched(want)
	if !in.acc.stats || len(names) == 0 {
		if len(in.samples) > 0 || in.statsOn {
			in.samples, in.statsOn = map[string]cpuSample{}, false
			return in.d.Emit.Replace(statsScope, integration.Facts{})
		}
		return nil
	}
	byName := map[string]summary{}
	for _, s := range in.listed {
		byName[s.name()] = s
	}
	var f integration.Facts
	seen := map[string]bool{}
	for _, name := range names {
		s, ok := byName[name]
		if !ok || s.State != "running" {
			continue
		}
		seen[s.ID] = true
		var st stats
		err := in.getJSON(ctx, in.api+"/containers/"+s.ID+"/stats?stream=false&one-shot=true", &st)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			in.d.Log.Debug("reading a container's stats failed", "container", name, "err", err)
			delete(in.samples, s.ID)
		}
		cpu, mem, limit := integration.Unknown(), integration.Unknown(), 0.0
		if err == nil {
			cpu = in.cpuShare(s.ID, st)
			mem, limit = memMiB(st)
		}
		id := in.rid("container/" + name)
		f.Metrics = append(f.Metrics,
			integration.Metric{Resource: id, Key: "cpu.pct", Unit: "%", Min: 0, Max: 100, Value: cpu},
			integration.Metric{Resource: id, Key: "mem.mib", Unit: "MiB", Min: 0, Max: limit, Value: mem})
	}
	for id := range in.samples {
		if !seen[id] {
			delete(in.samples, id)
		}
	}
	in.statsOn = true
	return in.d.Emit.Replace(statsScope, f)
}

// cpuShare is the container's share of the whole host's CPU time since the
// previous reading, 0–100; unknown on the first one. One-shot stats carry
// no previous sample of their own, so the integration keeps it.
func (in *instance) cpuShare(id string, st stats) float64 {
	now := cpuSample{total: st.CPU.Usage.Total, system: st.CPU.System}
	prev, ok := in.samples[id]
	in.samples[id] = now
	if !ok || now.system <= prev.system || now.total < prev.total {
		return integration.Unknown()
	}
	pct := float64(now.total-prev.total) / float64(now.system-prev.system) * 100
	return math.Round(min(pct, 100)*10) / 10
}

// memMiB is the memory the container uses, without the page cache it could
// give back (as docker stats counts it), and its limit; MiB. The limit is
// the host's memory when the container has none.
func memMiB(st stats) (used, limit float64) {
	m := st.Memory
	if m.Usage == 0 && m.Limit == 0 {
		return integration.Unknown(), 0
	}
	cache := m.Stats["inactive_file"] // cgroup v2
	if v, ok := m.Stats["total_inactive_file"]; ok {
		cache = v // cgroup v1
	}
	u := m.Usage
	if cache < u {
		u -= cache
	}
	const mib = 1 << 20
	return math.Round(float64(u) / mib), math.Round(float64(m.Limit) / mib)
}
