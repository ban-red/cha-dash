// Package realtime is the hub's WebSocket gateway: one multiplexed socket per
// browser tab, carrying any number of topic subscriptions.
//
// Protocol v1 (JSON text frames):
//
//	→ {"op":"hello","v":1,"resume":4810}        first frame, within 10 s
//	← {"op":"hello","v":1,"epoch":"…","server":"…","ping":30}
//	→ {"op":"sub","id":1,"topics":["graph","graph:kind/container"]}
//	← {"op":"snap","id":1,"seq":4812,"data":{"graph":{…},"graph:kind/container":{…}}}
//	← {"op":"diff","id":1,"seq":4813,"patch":[{"op":"replace","path":"/graph/resources/…","value":{…}}]}
//	← {"op":"snap","id":1,"seq":4900,"data":{…},"resync":true}   the client fell behind: replace state
//	→ {"op":"unsub","id":1}
//	← {"op":"ping"}                              every 30 s; the client watchdog reconnects on silence
//	→ {"op":"ping"}  ← {"op":"pong"}             optional client-initiated check
//	← {"op":"error","id":1,"code":"unknown_topic","msg":"…"}
//
// A subscription's state is one JSON document with a member per topic. The
// snap frame carries it whole; diff frames are RFC 6902 patches against it,
// coalesced to at most one per subscription per Coalesce interval (4/s by
// default). seq is the graph version: informational, for resume and
// diagnostics; clients apply every frame in order. epoch changes on every hub
// start, so a client can tell that a stored seq is from another run.
//
// resume is accepted but not acted on in v0: every sub gets an inline snapshot,
// so a reconnecting client is always correct, just not minimal.
package realtime

import "dash.cha.sh/internal/jsonpatch"

// ProtocolVersion is the only version this hub speaks.
const ProtocolVersion = 1

// clientMsg is any frame from the browser.
type clientMsg struct {
	Op     string   `json:"op"`
	V      int      `json:"v,omitempty"`
	Resume uint64   `json:"resume,omitempty"`
	ID     int64    `json:"id,omitempty"`
	Topics []string `json:"topics,omitempty"`
}

type helloMsg struct {
	Op     string `json:"op"`
	V      int    `json:"v"`
	Epoch  string `json:"epoch"`
	Server string `json:"server,omitempty"`
	Ping   int    `json:"ping"` // server ping interval, seconds
}

type snapMsg struct {
	Op     string         `json:"op"`
	ID     int64          `json:"id"`
	Seq    uint64         `json:"seq"`
	Data   map[string]any `json:"data"`
	Resync bool           `json:"resync,omitempty"`
}

type diffMsg struct {
	Op    string         `json:"op"`
	ID    int64          `json:"id"`
	Seq   uint64         `json:"seq"`
	Patch []jsonpatch.Op `json:"patch"`
}

type errorMsg struct {
	Op   string `json:"op"`
	ID   int64  `json:"id,omitempty"`
	Code string `json:"code"`
	Msg  string `json:"msg"`
}

type opMsg struct {
	Op string `json:"op"`
}

// Error codes sent in error frames.
const (
	codeBadRequest   = "bad_request"
	codeVersion      = "unsupported_version"
	codeUnknownOp    = "unknown_op"
	codeUnknownTopic = "unknown_topic"
	codeDuplicateID  = "duplicate_id"
	codeUnknownSub   = "unknown_sub"
	codeTooManySubs  = "too_many_subs"
)
