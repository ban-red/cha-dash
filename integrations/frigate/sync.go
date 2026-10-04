package frigate

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"dash.cha.sh/internal/integration"
)

// Scopes: each part refreshes at its own rate, so a series' as_of says when
// its source was last read.
const (
	scopeGraph  = "graph"  // the app and the cameras
	scopeStats  = "stats"  // inference.ms: /api/stats, or the WebSocket's stats push
	scopeEvents = "events" // detections.24h: /api/events
)

// hintID is the inbox hint for an API that grants admin without a sign-in.
const hintID = "unauthenticated-admin"

// Sync implements integration.Instance. Every sync reads /api/version (a
// few bytes; it's the liveness check). The rest only when due: the config
// and profile every 15 minutes, stats unless the WebSocket is pushing them,
// and events every 5 minutes while watched (30 while idle) or soon after
// the WebSocket saw a new object.
func (in *instance) Sync(ctx context.Context, want integration.Interest) (err error) {
	defer func() {
		if err != nil {
			in.mu.Lock()
			in.needFull = true
			in.mu.Unlock()
		}
	}()
	now := in.d.Now()
	version, err := in.fetchVersion(ctx)
	if err != nil {
		return err
	}

	in.mu.Lock()
	full := in.needFull
	needConfig := full || !in.haveConfig || in.configStale || now.Sub(in.configAt) >= configEvery
	every := in.statsEvery
	if every <= 0 {
		every = defaultStatsEvery
	}
	pushed := in.wsLive && in.statsFromWS && now.Sub(in.statsAt) < 3*every
	eventsEvery := eventsEveryIdle
	if want.Watched {
		eventsEvery = eventsEveryWatched
	}
	needEvents := full || in.eventsAt.IsZero() || now.Sub(in.eventsAt) >= eventsEvery ||
		(in.eventsDirty && now.Sub(in.eventsAt) >= eventsDebounce)
	in.mu.Unlock()

	var (
		cfg      frigateConfig
		prof     profile
		noProf   bool
		st       stats
		readStat = full || !pushed
		evs      []event
	)
	if needConfig {
		if prof, noProf, err = in.fetchProfile(ctx); err != nil {
			return err
		}
		if err = in.getJSON(ctx, "/api/config", nil, &cfg); err != nil {
			return err
		}
	}
	if readStat {
		if err = in.getJSON(ctx, "/api/stats", nil, &st); err != nil {
			return err
		}
		if st.Cameras == nil {
			return &integration.RepairError{Code: integration.CodeAPIChanged, Message: "GET /api/stats has no cameras: this Frigate's API isn't one this integration knows"}
		}
	}
	if needEvents {
		q := url.Values{
			"after":              {strconv.FormatInt(now.Add(-24*time.Hour).Unix(), 10)},
			"limit":              {strconv.Itoa(eventsLimit)},
			"include_thumbnails": {"0"},
		}
		if err = in.getJSON(ctx, "/api/events", q, &evs); err != nil {
			return err
		}
	}

	in.mu.Lock()
	defer in.mu.Unlock()
	in.version = version
	in.d.Emit.SetVersion(semver(version))
	if needConfig {
		in.anonymous = in.anonymousAdmin(prof, noProf)
		in.allowed = nil
		if !noProf && prof.Role != "admin" && prof.AllowedCameras != nil {
			in.allowed = prof.AllowedCameras
		}
		in.applyConfig(cfg, now)
	}
	if readStat {
		in.applyStats(st, now, false)
	}
	if needEvents {
		in.applyEvents(evs, now)
	}
	in.needFull, in.synced = false, true
	if err := in.d.Emit.Replace(scopeGraph, in.graphFacts()); err != nil {
		return err
	}
	if readStat {
		if err := in.d.Emit.Replace(scopeStats, in.statsFacts()); err != nil {
			return err
		}
	}
	if needEvents || needConfig {
		// A changed camera set changes the series; the counts stay as read.
		if err := in.d.Emit.Replace(scopeEvents, in.eventsFacts()); err != nil {
			return err
		}
	}
	return in.hint()
}

// hint raises the inbox hint while Frigate grants admin without a sign-in,
// and resolves it once Cha Dash signs in (spike 4.5). Adding credentials
// restarts the instance but keeps its emitter, so the new instance resolves
// what the old one raised; resolving a hint that isn't open does nothing.
func (in *instance) hint() error {
	if !in.anonymous {
		return in.d.Emit.Resolve(hintID, fmt.Sprintf("%s no longer grants admin without a sign-in.", in.name))
	}
	self := in.d.Emit.Self()
	where := "Frigate's internal port"
	if p := port(in.base); p != internalPort {
		where = "a Frigate port"
	}
	return in.d.Emit.Raise(integration.Incident{
		ID: hintID, Scenario: "frigate-unauthenticated-admin", Class: integration.ClassProblem, Severity: integration.SeverityInfo,
		Category: integration.CategorySecurity,
		Title:    in.name + " grants admin without a sign-in",
		Summary:  fmt.Sprintf("Cha Dash reads %s through %s (%s), which answers without a sign-in and makes anyone who can reach it an admin: they can change the config, restart Frigate and delete recordings.", in.name, where, port(in.base)),
		Target:   in.appID,
		Facts:    map[string]any{"port": port(in.base), "username": "anonymous", "role": "admin"},
		Suggested: integration.Action{
			ID: "integration.settings", Label: "Use the authenticated port", Tier: "T0", Target: self,
			Detail: "Create a Frigate user with the viewer role, then point this instance at Frigate's authenticated port (8971) with that user.",
		},
		ResolvesWhen: fmt.Sprintf("Resolves when Cha Dash signs in to %s.", in.name),
	})
}

// Test implements integration.Instance: the version, and the role Frigate
// gives this connection.
func (in *instance) Test(ctx context.Context) integration.TestResult {
	version, err := in.fetchVersion(ctx)
	if err != nil {
		return integration.Failed(err)
	}
	prof, noProf, err := in.fetchProfile(ctx)
	if err != nil {
		return integration.Failed(err)
	}
	res := integration.TestResult{OK: true, Version: semver(version)}
	who := "user " + prof.Username
	if prof.Username == "anonymous" {
		who = "anonymous, without a sign-in"
	}
	cams := ""
	if n := len(prof.AllowedCameras); n > 0 {
		cams = fmt.Sprintf("; %d %s", n, plural(n, "camera"))
	}
	switch {
	case noProf:
		res.Grants = append(res.Grants, integration.Grant{Name: "Frigate API", Access: integration.AccessAdmin,
			Detail: "This Frigate has no users or roles (it's older than 0.14): anyone who reaches it can change its config"})
	case prof.Role == "admin":
		res.Grants = append(res.Grants, integration.Grant{Name: "Frigate admin", Access: integration.AccessAdmin,
			Detail: who + ": can change the config, restart Frigate and delete recordings" + cams})
	case prof.Role == "viewer":
		res.Grants = append(res.Grants, integration.Grant{Name: "Frigate viewer", Access: integration.AccessRead, Detail: who + cams})
	default:
		res.Grants = append(res.Grants, integration.Grant{Name: "Frigate role " + prof.Role, Access: integration.AccessRead, Detail: who + cams})
	}
	if in.anonymousAdmin(prof, noProf) {
		res.Warnings = append(res.Warnings, integration.Warning{Code: "frigate_unauthenticated", Severity: integration.WarnWarning,
			Message: "This address answers without a sign-in and grants admin to anyone who can reach it. Use Frigate's authenticated port (8971) with a viewer account."})
	}
	return res
}
