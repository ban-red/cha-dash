package apidoc

import (
	"encoding/json"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/history"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/notify"
	"dash.cha.sh/internal/specstore"
	"dash.cha.sh/internal/topics"
)

// Mirrors of what internal/notify's and internal/specstore's handlers, and
// the reads in internal/server (reads.go, history.go), read and write.
type (
	channelList struct {
		Channels  []notify.Channel `json:"channels"`
		PublicURL bool             `json:"public_url"`
		WebPush   bool             `json:"web_push"`
	}
	notifyInvalid struct {
		Error  httpx.ErrorDetail   `json:"error"`
		Fields []notify.FieldError `json:"fields"`
	}
	ruleList struct {
		Rules []notify.Rule `json:"rules"`
	}
	deliveryList struct {
		Deliveries []notify.Delivery `json:"deliveries"`
	}
	pushKey struct {
		PublicKey string `json:"public_key"`
	}
	deviceList struct {
		Subscriptions []notify.Device `json:"subscriptions"`
	}
	endpointBody struct {
		Endpoint string `json:"endpoint"`
	}

	specList struct {
		Specs []specstore.Installed `json:"specs"`
	}
	specInstall struct {
		YAML    string `json:"yaml"`
		Source  string `json:"source,omitempty"`
		Replace bool   `json:"replace,omitempty"`
	}
	specProblem struct {
		Error   *specstore.Problem `json:"error"`
		Preview *specstore.Preview `json:"preview,omitempty"`
	}
	specActions struct {
		On     *bool  `json:"on"`
		SHA256 string `json:"sha256,omitempty"`
	}

	graphBody struct {
		Version   uint64                        `json:"version"`
		Resources map[graph.RID]*graph.Resource `json:"resources"`
		Relations map[string]*graph.Relation    `json:"relations"`
	}
	// inboxBody is the inbox topic's document: incidents are the topic's
	// free-form objects (internal/README.md, "Realtime topics").
	inboxBody struct {
		Now       time.Time                  `json:"now"`
		Incidents map[string]json.RawMessage `json:"incidents"`
	}
	causesBody struct {
		Resource string         `json:"resource"`
		Causes   []topics.Cause `json:"causes"`
	}
	historySeries struct {
		Unit  string     `json:"unit"`
		Start int64      `json:"start"`
		Step  int64      `json:"step"`
		Avg   []*float32 `json:"avg"`
		Min   []*float32 `json:"min"`
		Max   []*float32 `json:"max"`
	}
	historyBody struct {
		Level  history.Level            `json:"level"`
		Series map[string]historySeries `json:"series"`
	}
	stateChange struct {
		At     int64  `json:"at"`
		Status string `json:"status"`
	}
	statesBody struct {
		Level  history.Level    `json:"level"`
		States []stateChange    `json:"states"`
		Totals map[string]int64 `json:"totals"`
		Up     *float64         `json:"up"`
	}
	pulseSlot struct {
		Counts map[string]int    `json:"counts"`
		Issues map[string]string `json:"issues"`
	}
	pulseBody struct {
		Level history.Level `json:"level"`
		Start int64         `json:"start"`
		Step  int64         `json:"step"`
		Slots []pulseSlot   `json:"slots"`
	}
)

func init() {
	invalid := map[int]any{422: notifyInvalid{}}
	add("notify", map[string]Op{
		"GET /api/notify/channels":                   {Summary: "The notification channels, and whether the hub has a public URL and Web Push.", Resp: channelList{}},
		"POST /api/notify/channels":                  {Summary: "Add a notification channel.", Body: notify.ChannelInput{}, Status: 201, Resp: notify.Channel{}, Fail: invalid},
		"PUT /api/notify/channels/{id}":              {Summary: "Change a notification channel.", Body: notify.ChannelInput{}, Resp: notify.Channel{}, Fail: invalid},
		"DELETE /api/notify/channels/{id}":           {Summary: "Remove a notification channel.", Errors: []string{"builtin"}},
		"POST /api/notify/channels/{id}/test":        {Summary: "Send a test notification through a channel.", Resp: notify.TestResult{}, Errors: []string{"no_devices"}},
		"GET /api/notify/rules":                      {Summary: "The notification rules.", Resp: ruleList{}},
		"PUT /api/notify/rules/{id}":                 {Summary: "Change a notification rule.", Body: notify.RuleUpdate{}, Resp: notify.Rule{}, Fail: invalid, Errors: []string{"locked"}},
		"GET /api/notify/deliveries":                 {Summary: "The delivery log, newest first.", Resp: deliveryList{}, Query: []Param{{Name: "state", Desc: "pending, sent, failed or dropped."}, {Name: "limit", Desc: "1 to 200, 50 by default."}}},
		"GET /api/notify/push/key":                   {Summary: "The hub's VAPID public key, for Web Push.", Resp: pushKey{}},
		"GET /api/notify/push/subscriptions":         {Summary: "Your devices' push subscriptions.", Resp: deviceList{}},
		"POST /api/notify/push/subscriptions":        {Summary: "Subscribe this device to push notifications.", Body: notify.SubscribeInput{}, Status: 201, Resp: notify.Device{}, Fail: invalid},
		"DELETE /api/notify/push/subscriptions/{id}": {Summary: "Remove one of your devices."},
		"POST /api/notify/push/unsubscribe":          {Summary: "Remove this device's subscription by its endpoint.", Body: endpointBody{}},
	})
	add("specs", map[string]Op{
		"GET /api/specs/installed":              {Summary: "The specs the owner installed.", Resp: specList{}},
		"POST /api/specs/installed/preview":     {Summary: "Check a spec and preview what installing it would add, saving nothing.", Body: specInstall{}, Resp: specstore.Preview{}, Fail: map[int]any{400: specProblem{}, 409: specProblem{}}},
		"POST /api/specs/installed":             {Summary: "Install a spec, or replace one. Needs a step-up.", Body: specInstall{}, Resp: specstore.Installed{}, Fail: map[int]any{400: specProblem{}, 409: specProblem{}}, Errors: []string{"step_up_required"}},
		"DELETE /api/specs/installed/{id}":      {Summary: "Remove an installed spec.", Resp: specstore.Installed{}, Fail: map[int]any{409: specProblem{}}},
		"PUT /api/specs/installed/{id}/actions": {Summary: "Turn a spec's actions on (for the version reviewed; needs a step-up) or off.", Body: specActions{}, Resp: specstore.Installed{}, Fail: map[int]any{409: specProblem{}}},
	})
	add("reads", map[string]Op{
		"GET /api/graph":  {Summary: "The Home Graph as your role sees it.", Resp: graphBody{}},
		"GET /api/inbox":  {Summary: "The Inbox as your role sees it.", Resp: inboxBody{}},
		"GET /api/causes": {Summary: "One resource's cause hints, with their paths.", Query: []Param{{Name: "resource", Desc: "A resource ID.", Required: true}}, Resp: causesBody{}},
		"GET /api/history": {Summary: "Metrics history: points of up to 20 series over a range.", Resp: historyBody{}, Query: []Param{
			{Name: "series", Desc: "<resource>#<metric>, 1 to 20 times.", Required: true, Many: true},
			{Name: "from", Desc: "Unix milliseconds; 24 hours before to by default."}, {Name: "to", Desc: "Unix milliseconds; now by default."},
			{Name: "points", Desc: "1 to 2000."}}},
		"GET /api/history/states": {Summary: "A resource's health changes over a range, and the time in each.", Resp: statesBody{}, Query: []Param{
			{Name: "resource", Desc: "A resource ID.", Required: true},
			{Name: "from", Desc: "Unix milliseconds; a week before to by default."}, {Name: "to", Desc: "Unix milliseconds; now by default."}}},
		"GET /api/history/pulse": {Summary: "The whole home's health over a range, in slots.", Resp: pulseBody{}, Query: []Param{
			{Name: "from", Desc: "Unix milliseconds; 24 hours before to by default."}, {Name: "to", Desc: "Unix milliseconds; now by default."},
			{Name: "points", Desc: "1 to 288."}}},
	})
}
