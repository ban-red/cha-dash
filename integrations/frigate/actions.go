package frigate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"dash.cha.sh/integrations/internal/act"
	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/integration"
)

// Actions (v0.2): turn a camera, or its detection, recording, snapshots or
// review alerts, on and off. All T1, each direction the other's undo. They
// act with the control credential (control_username, control_password), a
// Frigate admin: Frigate takes these changes from admins only. Execute opens
// a /ws connection of its own, sends the one frame
// {"topic":"<camera>/<feature>/set","payload":"ON"|"OFF"} and waits for
// Frigate to publish <camera>/<feature>/state with the new value. No other
// topic is ever sent.

// Action IDs.
const (
	ActDetectOn     = "frigate.camera.detect.on"
	ActDetectOff    = "frigate.camera.detect.off"
	ActRecordOn     = "frigate.camera.record.on"
	ActRecordOff    = "frigate.camera.record.off"
	ActEnabledOn    = "frigate.camera.enabled.on"
	ActEnabledOff   = "frigate.camera.enabled.off"
	ActSnapshotsOn  = "frigate.camera.snapshots.on"
	ActSnapshotsOff = "frigate.camera.snapshots.off"
	ActAlertsOn     = "frigate.camera.alerts.on"
	ActAlertsOff    = "frigate.camera.alerts.off"
)

// actionTimeout bounds Execute: sign-in, the socket and the wait.
const actionTimeout = 30 * time.Second

// stateWait is how long Execute waits for Frigate to publish the new
// state. Tests shorten it.
var stateWait = 10 * time.Second

// enabledSince is the first Frigate that turns a whole camera on and off at
// runtime (<camera>/enabled/set).
var enabledSince = [2]int{0, 16}

// cameraName is Frigate's own rule for camera names. A key that doesn't
// match never makes it into a topic.
var cameraName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// feature is one switch on a camera.
type feature struct {
	id    string // the action ID's third part
	topic string // Frigate's: <camera>/<topic>/set and …/state
	noun  string // "detection"; "" for the whole camera
	// inConfig says whether Frigate's config file lets it be turned on at
	// runtime; nil when Frigate allows it either way.
	inConfig func(camState) bool
	state    func(camState) bool
}

var features = []feature{
	{id: "detect", topic: "detect", noun: "detection", state: func(c camState) bool { return c.detect }},
	{id: "record", topic: "recordings", noun: "recording", state: func(c camState) bool { return c.record },
		inConfig: func(c camState) bool { return c.recordInConfig }},
	{id: "enabled", topic: "enabled", state: func(c camState) bool { return c.enabled },
		inConfig: func(c camState) bool { return c.enabledInConfig }},
	{id: "snapshots", topic: "snapshots", noun: "snapshots", state: func(c camState) bool { return c.snapshots }},
	{id: "alerts", topic: "review_alerts", noun: "review alerts", state: func(c camState) bool { return c.alerts },
		inConfig: func(c camState) bool { return c.alertsInConfig }},
}

// actionOf is the feature and direction an action ID names.
func actionOf(id string) (feature, bool, bool) {
	rest, ok := strings.CutPrefix(id, ID+".camera.")
	if !ok {
		return feature{}, false, false
	}
	name, dir, _ := strings.Cut(rest, ".")
	i := slices.IndexFunc(features, func(f feature) bool { return f.id == name })
	if i < 0 || (dir != "on" && dir != "off") {
		return feature{}, false, false
	}
	return features[i], dir == "on", true
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// verb is "Turn on detection for" or "Turn off" (the whole camera): what
// comes before the camera's name.
func (f feature) verb(on bool) string {
	if f.noun == "" {
		return "Turn " + onOff(on)
	}
	return "Turn " + onOff(on) + " " + f.noun + " for"
}

// Actions implements integration.Actor.
func (in *instance) Actions() []action.Def {
	var defs []action.Def
	for _, f := range features {
		for _, on := range []bool{true, false} {
			id, undo := ID+".camera."+f.id+"."+onOff(on), ID+".camera."+f.id+"."+onOff(!on)
			defs = append(defs, action.Def{
				ID: id, Title: strings.TrimSuffix(f.verb(on), " for"), Targets: []string{"camera"}, Tier: action.T1,
				Timeout:       actionTimeout,
				Resolve:       in.resolveCamera,
				Preconditions: in.preconditions,
				Permitted:     in.permitted,
				Label:         func(r action.Request) string { return f.verb(on) + " " + r.Target.Name },
				Steps: func(r action.Request) []action.Step {
					return act.Steps(f.verb(on)+" "+r.Target.Name+" in Frigate", "Wait for Frigate to report it "+onOff(on))
				},
				Undo: act.UndoBy(undo, f.verb(!on)),
			})
		}
	}
	return defs
}

// camState is a camera as an action reads it from /api/config, which
// Frigate keeps in step with what's been switched at runtime.
type camState struct {
	name                                       string
	enabled, detect, record, snapshots, alerts bool
	// What the config file says; true when Frigate doesn't say.
	enabledInConfig, recordInConfig, alertsInConfig bool
}

// liveState is what the actions read: the version and the cameras this
// instance publishes.
type liveState struct {
	version string
	cams    map[string]camState
}

// readLive reads the version, the profile and the config, once per
// listing (action.Memo).
func (in *instance) readLive(ctx context.Context) (liveState, error) {
	return action.Memo(ctx, ID+"/"+in.key+"/live", func(ctx context.Context) (liveState, error) {
		version, err := in.fetchVersion(ctx)
		if err != nil {
			return liveState{}, err
		}
		prof, noProf, err := in.fetchProfile(ctx)
		if err != nil {
			return liveState{}, err
		}
		var cfg frigateConfig
		if err := in.getJSON(ctx, "/api/config", nil, &cfg); err != nil {
			return liveState{}, err
		}
		var allowed []string
		if !noProf && prof.Role != "admin" {
			allowed = prof.AllowedCameras
		}
		ls := liveState{version: semver(version), cams: map[string]camState{}}
		for key, cc := range cfg.Cameras {
			if len(allowed) > 0 && !slices.Contains(allowed, key) {
				continue
			}
			name := cc.FriendlyName
			if name == "" {
				name = humanize(key)
			}
			ls.cams[key] = camState{
				name:            name,
				enabled:         boolOr(cc.Enabled, true),
				detect:          boolOr(cc.Detect.Enabled, true),
				record:          boolOr(cc.Record.Enabled, false),
				snapshots:       boolOr(cc.Snapshots.Enabled, false),
				alerts:          boolOr(cc.Review.Alerts.Enabled, true),
				enabledInConfig: boolOr(cc.EnabledInConfig, true),
				recordInConfig:  boolOr(cc.Record.EnabledInConfig, true),
				alertsInConfig:  boolOr(cc.Review.Alerts.EnabledInConfig, true),
			}
		}
		return ls, nil
	})
}

// readRefusal is a failed read as a refusal the listing shows.
func readRefusal(err error) error {
	_, _, msg := integration.Classify(err)
	return action.Unavailable("Cha Dash couldn't read Frigate just now: "+msg+".", "")
}

// cameraKey is the camera name in one of this instance's camera IDs.
func (in *instance) cameraKey(target string) (string, bool) {
	key, ok := strings.CutPrefix(target, in.prefix+"/camera/")
	return key, ok && cameraName.MatchString(key)
}

// resolveCamera resolves one of this instance's cameras with a fresh read.
// Ref is Frigate's camera name.
func (in *instance) resolveCamera(ctx context.Context, target string) (action.Target, error) {
	key, ok := in.cameraKey(target)
	if !ok {
		return action.Target{}, action.ErrNotFound
	}
	ls, err := in.readLive(ctx)
	if err != nil {
		return action.Target{}, readRefusal(err)
	}
	c, ok := ls.cams[key]
	if !ok {
		return action.Target{}, action.ErrNotFound
	}
	fp := []string{"camera", c.name}
	for _, f := range features {
		fp = append(fp, f.id+"="+onOff(f.state(c)))
	}
	return action.Target{ID: target, Kind: "camera", Name: c.name, State: act.Fingerprint(fp...), Ref: key}, nil
}

// preconditions: the camera is there, isn't already in that state, its
// config lets Frigate turn the feature on, and Frigate is new enough to
// turn a whole camera on and off.
func (in *instance) preconditions(ctx context.Context, req action.Request) error {
	f, on, ok := actionOf(req.Action)
	if !ok {
		return action.ErrNotFound
	}
	ls, err := in.readLive(ctx)
	if err != nil {
		return readRefusal(err)
	}
	c, ok := ls.cams[req.Target.Ref]
	if !ok {
		return action.Precondition("Frigate no longer has " + req.Target.Name + ".")
	}
	if f.id == "enabled" && versionBefore(ls.version, enabledSince) {
		return action.Precondition(fmt.Sprintf("Turning a whole camera on or off needs Frigate %d.%d or later; this one runs %s.", enabledSince[0], enabledSince[1], ls.version))
	}
	if f.state(c) == on {
		if f.noun == "" {
			return action.Precondition(c.name + " is already " + onOff(on) + ".")
		}
		return action.Precondition(strings.ToUpper(f.noun[:1]) + f.noun[1:] + " is already " + onOff(on) + " for " + c.name + ".")
	}
	if on && f.inConfig != nil && !f.inConfig(c) {
		if f.noun == "" {
			return action.Precondition(c.name + " is turned off in Frigate's config file, and Frigate only turns a camera on when its config enables it.")
		}
		return action.Precondition(strings.ToUpper(f.noun[:1]) + f.noun[1:] + " is off in " + c.name + "'s Frigate config, and Frigate only turns it on when the config enables it.")
	}
	return nil
}

// versionBefore reports whether version (0.15.1) is older than min. An
// unreadable version isn't.
func versionBefore(version string, min [2]int) bool {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	return major < min[0] || (major == min[0] && minor < min[1])
}

// permitted runs on the control instance: the control account must be a
// Frigate admin (or Frigate has no roles at all).
func (in *instance) permitted(ctx context.Context, _ action.Request) error {
	type who struct {
		p      profile
		noProf bool
	}
	w, err := action.Memo(ctx, ID+"/"+in.key+"/control/profile", func(ctx context.Context) (who, error) {
		p, noProf, err := in.fetchProfile(ctx)
		return who{p, noProf}, err
	})
	var ae *integration.AuthError
	switch {
	case errors.As(err, &ae):
		return action.Unavailable("Frigate rejected "+in.name+"'s control username or password. Enter them again in the instance's settings.", in.key)
	case err != nil:
		return readRefusal(err)
	case w.noProf || w.p.Role == "admin":
		return nil
	}
	return action.Unavailable(fmt.Sprintf("%s's control account, %s, has the %s role in Frigate. Frigate only lets an admin turn cameras and their features on and off.",
		in.name, w.p.Username, w.p.Role), in.key)
}

// setFrame is the one frame an action sends.
func setFrame(camera string, f feature, on bool) []byte {
	payload := "OFF"
	if on {
		payload = "ON"
	}
	b, _ := json.Marshal(map[string]any{"topic": camera + "/" + f.topic + "/set", "payload": payload, "retain": false})
	return b
}

// Execute implements integration.Actor, on the control instance: sign in,
// open /ws, send the set frame, and wait for Frigate to publish the new
// state.
func (in *instance) Execute(ctx context.Context, req action.Request, run *action.TaskRun) error {
	f, on, ok := actionOf(req.Action)
	key := req.Target.Ref
	if !ok || !cameraName.MatchString(key) {
		return action.Fail("invalid", "Cha Dash doesn't know that camera action.")
	}
	run.Step(f.verb(on) + " " + req.Target.Name + " in Frigate")
	conn, _, err := websocket.Dial(ctx, in.base.JoinPath("ws").String(), &websocket.DialOptions{HTTPClient: in.http})
	var ae *integration.AuthError
	switch {
	case errors.As(err, &ae):
		return action.Fail("auth", "Frigate rejected the control username or password. Enter them again in the instance's settings.")
	case err != nil:
		return action.Fail("unreachable", "Cha Dash couldn't open Frigate's WebSocket.")
	}
	defer conn.CloseNow()
	conn.SetReadLimit(wsReadLimit)
	if err := conn.Write(ctx, websocket.MessageText, setFrame(key, f, on)); err != nil {
		return action.Fail("unreachable", "Frigate closed the WebSocket before taking the change.")
	}
	run.Step("Wait for Frigate to report it " + onOff(on))
	wait, cancel := context.WithTimeout(ctx, stateWait)
	defer cancel()
	want := key + "/" + f.topic + "/state"
	for {
		typ, data, err := conn.Read(wait)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			what := f.noun
			if what == "" {
				what = "the camera"
			}
			return action.Fail("verify_failed", fmt.Sprintf("Frigate didn't report %s %s for %s within %d seconds. Check that the control account is a Frigate admin and that %s's config allows it.",
				what, onOff(on), req.Target.Name, int(stateWait/time.Second), req.Target.Name))
		}
		if typ != websocket.MessageText {
			continue
		}
		var msg struct {
			Topic   string          `json:"topic"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(data, &msg) != nil || msg.Topic != want {
			continue
		}
		if strings.EqualFold(payload(msg.Payload), onOff(on)) {
			run.Logf("Frigate reports %s %s", want, strings.ToUpper(onOff(on)))
			conn.Close(websocket.StatusNormalClosure, "")
			return nil
		}
	}
}
