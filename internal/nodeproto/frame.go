package nodeproto

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Frame is one message on the channel: JSON, in the browser protocol's
// "op" style.
//
// Control, once per connection, right after the handshake:
//
//	node → {"op":"hello","data":Hello}                  always first, within HelloTimeout
//	hub  → {"op":"welcome","data":Welcome}              a session follows
//	hub  → {"op":"refused","error":{"code","message"}}  then the hub closes
//
// RPC, in either direction (the hub calls node verbs such as
// container.restart; the node calls a few hub verbs such as node.rotate):
//
//	→ {"op":"req","id":1,"method":"container.logs","data":{…}}
//	← {"op":"item","id":1,"data":…}                     zero or more: a stream (progress, log lines)
//	← {"op":"res","id":1,"data":…}  or  {"op":"err","id":1,"error":{"code","message"}}
//	→ {"op":"cancel","id":1}                            the caller gave up; the handler's context ends
//
// Keepalive, in either direction, every KeepAlive:
//
//	→ {"op":"ping"}   ← {"op":"pong"}
//
// IDs are per direction: each side numbers the requests it sends.
type Frame struct {
	Op     string          `json:"op"`
	ID     uint64          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Frame ops.
const (
	OpHello   = "hello"
	OpWelcome = "welcome"
	OpRefused = "refused"
	OpReq     = "req"
	OpItem    = "item"
	OpRes     = "res"
	OpErr     = "err"
	OpCancel  = "cancel"
	OpPing    = "ping"
	OpPong    = "pong"
)

// Error is a failure the other side reported, with a stable code. Messages
// are sentences a person can read; they never carry secrets.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

// ErrorCode is the code, for packages that see the error only through an
// interface (integrations can't import this package).
func (e *Error) ErrorCode() string { return e.Code }

// CodeNotConnected is the hub's answer for a node without a live session.
const CodeNotConnected = "not_connected"

// Error codes on the wire.
const (
	// CodeForbidden: the node's local policy doesn't allow the verb, or the
	// target is protected.
	CodeForbidden = "forbidden"
	// CodeNotFound: no such verb, or no such target.
	CodeNotFound = "not_found"
	// CodeInvalid: the request didn't parse or failed validation.
	CodeInvalid = "invalid"
	// CodeConflict: the target isn't in a state the verb makes sense for.
	CodeConflict = "conflict"
	// CodeUnavailable: the node can't reach what the verb needs (Docker).
	CodeUnavailable = "unavailable"
	// CodeBusy: too many requests in flight on this session.
	CodeBusy = "busy"
	// CodeFailed: anything else.
	CodeFailed = "failed"

	// Refusal codes for the control phase.
	CodeUnknownNode = "unknown_node" // a key the hub doesn't know, and nothing to enrol with
	CodePending     = "pending"      // enrolled, waiting for an admin to approve it: retry later
	CodeRevoked     = "revoked"
	CodeBadPairing  = "bad_pairing" // the pairing secret didn't match
	CodeRateLimited = "rate_limited"
	CodeProtocol    = "protocol"
)

// Errorf returns an *Error.
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// CodeOf is err's code: an *Error's own, or CodeFailed.
func CodeOf(err error) string {
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Code
	}
	return CodeFailed
}

// Hello is the node's first frame.
type Hello struct {
	// Versions the node speaks (ALPN already chose one; this is for logs).
	Versions []int `json:"versions"`
	// Name is what the node calls itself (CHADASH_NODE_NAME).
	Name string `json:"name"`
	// Build is the node binary's version.
	Build string `json:"build,omitempty"`
	// Node is the ID the hub gave it, once enrolled.
	Node string `json:"node,omitempty"`
	// Pairing is the local pairing secret, sent only by a node that isn't
	// enrolled yet, and only after the hub proved the pinned key.
	Pairing string `json:"pairing,omitempty"`
	// Token is the secret of a join token (cdj_<secret>.<hub pin>), sent
	// only by a node that isn't enrolled yet, and only after the hub proved
	// the token's pin. It carries the secret alone; the pin stays on the
	// node, as with Pairing. The hub refuses it until token enrollment
	// lands.
	Token string `json:"token,omitempty"`
	// HubPins are the hub keys the node trusts (also on the upgrade, as
	// PinsHeader): the hub records them, so it knows who has its next key.
	HubPins []string `json:"hub_pins,omitempty"`
	// Verbs are what the node serves and whether its policy allows each.
	Verbs []VerbInfo `json:"verbs,omitempty"`
}

// VerbInfo describes one verb in Hello.
type VerbInfo struct {
	Name    string `json:"name"`
	Policy  string `json:"policy"`
	Allowed bool   `json:"allowed"`
}

// Welcome is the hub admitting a node.
type Welcome struct {
	Node string `json:"node"`
	// HubPin is the hub's newest key. A node without it receives it as a
	// hub.pin request too.
	HubPin string `json:"hub_pin"`
}

// Built-in verbs.
const (
	// VerbHubPin is the hub pushing its next key to a node: {"pin"}.
	VerbHubPin = "hub.pin"
	// VerbNodeRotate is a node announcing its next key: {"next"} (a pin).
	VerbNodeRotate = "node.rotate"
)

// PinMsg is hub.pin's request.
type PinMsg struct {
	Pin string `json:"pin"`
}

// RotateMsg is node.rotate's request.
type RotateMsg struct {
	Next string `json:"next"`
}

// PinsHeader carries the hub pins a node accepts on the WebSocket upgrade,
// comma-separated, so the hub answers with a key the node trusts during a
// rollover. Pins are public: a proxy seeing them learns nothing it can use.
const PinsHeader = "Chadash-Hub-Pins"

// Path is where the hub serves nodes, on its own listener.
const Path = "/node"

// Local pairing (v0.2): the hub writes a pairing token into a volume it
// shares with the node on the compose network. Like v0.3's join tokens, it
// carries the hub's pin, so the node checks the hub's key before it sends
// the secret, and pairing isn't trust-on-first-use.
const pairingPrefix = "cdp_"

// FormatPairing builds a pairing token: cdp_<secret>.<hub pin>.
func FormatPairing(secret, hubPin string) string { return pairingPrefix + secret + "." + hubPin }

// ParsePairing splits a pairing token into its secret and the hub's pin.
func ParsePairing(tok string) (secret, hubPin string, err error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(tok), pairingPrefix)
	secret, hubPin, ok2 := strings.Cut(rest, ".")
	if !ok || !ok2 || len(secret) < 16 || !ValidPin(hubPin) {
		return "", "", errors.New("nodeproto: malformed pairing token (want cdp_<secret>.<hub pin>)")
	}
	return secret, hubPin, nil
}

// Join tokens (v0.3): an admin makes one in Settings → Nodes for a node on
// another host. Like a pairing token it carries the hub's pin; unlike one,
// it's single-use, and the node it enrols waits for approval.
const joinPrefix = "cdj_"

// FormatJoin builds a join token: cdj_<secret>.<hub pin>.
func FormatJoin(secret, hubPin string) string { return joinPrefix + secret + "." + hubPin }

// ParseJoin splits a join token into its secret and the hub's pin. A
// pairing token is not a join token.
func ParseJoin(tok string) (secret, hubPin string, err error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(tok), joinPrefix)
	secret, hubPin, ok2 := strings.Cut(rest, ".")
	if !ok || !ok2 || len(secret) < 16 || !ValidPin(hubPin) {
		return "", "", errors.New("nodeproto: malformed join token (want cdj_<secret>.<hub pin>)")
	}
	return secret, hubPin, nil
}
