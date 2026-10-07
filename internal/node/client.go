package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	"dash.cha.sh/internal/nodeproto"
)

// Config configures a Client.
type Config struct {
	// HubURL is ws://chadash:3696/node on the compose network, or
	// wss://dash.example/node through a reverse proxy.
	HubURL string
	// Name is what the hub calls the node ("local" for the hub's own host).
	Name string
	// Build is the binary's version, reported in the hello.
	Build    string
	Identity *IdentityStore
	// Dispatcher serves the hub's requests under the policy.
	Dispatcher *Dispatcher
	// Pairing is the local pairing token (cdp_<secret>.<hub pin>), or "".
	// Its pin is trusted; its secret is sent only while the node isn't
	// enrolled.
	Pairing string
	// PairingFile, when set, is re-read each time the node is about to
	// pair, so a secret the hub regenerated (it's single-use) reaches the
	// node without a restart. Only its secret is used: the hub keys the
	// node trusts change through the channel, never through this file.
	PairingFile string
	// HTTPClient makes the outer connection; nil is http.DefaultClient.
	HTTPClient *http.Client
	Session    nodeproto.SessionOptions
	Log        *slog.Logger
	// Health, when set, gets the connection state.
	Health *Health
}

// Client keeps the node connected to its hub.
type Client struct {
	cfg    Config
	log    *slog.Logger
	policy Policy
	secret string // from the pairing token

	mu      sync.Mutex
	sess    *nodeproto.Session
	repair  bool // the hub forgot us: pair again
	backoff time.Duration
}

// Connection states, for Health and the healthcheck.
const (
	StateConnecting = "connecting"
	StateConnected  = "connected"
	StateRefused    = "refused"
)

// NewClient checks the config. The pairing token's pin joins the trusted
// hub keys, unless the policy fixes the hub's pin.
func NewClient(cfg Config) (*Client, error) {
	if cfg.HubURL == "" {
		return nil, errors.New("node: no hub URL (CHADASH_NODE_HUB or hub.url in the policy)")
	}
	if cfg.Identity == nil || cfg.Dispatcher == nil {
		return nil, errors.New("node: a client needs an identity and a dispatcher")
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	c := &Client{cfg: cfg, log: cfg.Log, policy: cfg.Dispatcher.Policy()}
	if cfg.Pairing != "" {
		secret, pin, err := nodeproto.ParsePairing(cfg.Pairing)
		if err != nil {
			return nil, err
		}
		c.secret = secret
		if c.policy.Hub.Pin == "" && !slices.Contains(cfg.Identity.State().HubPins, pin) {
			if err := cfg.Identity.AddHubPin(pin); err != nil {
				return nil, err
			}
		}
	}
	if len(c.pins()) == 0 {
		return nil, errors.New("node: no hub key to trust: set hub.pin in the policy, or give the node a pairing token")
	}
	if st := cfg.Identity.State(); st.HubURL != cfg.HubURL {
		if err := cfg.Identity.Update(func(s *State) { s.HubURL = cfg.HubURL }); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// pins are the hub keys the node trusts: the policy's, or those it saved.
func (c *Client) pins() []string {
	if c.policy.Hub.Pin != "" {
		return []string{c.policy.Hub.Pin}
	}
	return c.cfg.Identity.State().HubPins
}

// Run keeps the node connected until ctx ends, reconnecting with capped
// exponential backoff. A refusal (revoked, unknown) waits longer but never
// stops the node, so re-pairing it needs no restart.
func (c *Client) Run(ctx context.Context) error {
	c.log.Info("node identity", "fingerprint", nodeproto.Fingerprint(c.cfg.Identity.Current().Pin), "hub", c.cfg.HubURL)
	c.backoff = time.Second
	for {
		c.cfg.Health.Set(StateConnecting, "")
		wait, err := c.connectOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			c.log.Warn("not connected to the hub", "err", err, "retry_in", wait)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}

// connectOnce dials, says hello and serves the session until it ends. It
// returns how long to wait before the next attempt.
func (c *Client) connectOnce(ctx context.Context) (time.Duration, error) {
	id := c.cfg.Identity.Current()
	next, rotating := c.cfg.Identity.Next()
	if rotating {
		id = next // try the next key first: the hub may already hold it
	}
	ch, err := nodeproto.Dial(ctx, c.cfg.HubURL, nodeproto.DialOptions{Identity: id, Pins: c.pins(), HTTPClient: c.cfg.HTTPClient})
	if err != nil {
		return c.grow(), err
	}
	st := c.cfg.Identity.State()
	c.mu.Lock()
	repair := c.repair
	c.mu.Unlock()
	hello := nodeproto.Hello{
		Versions: nodeproto.Versions, Name: c.cfg.Name, Build: c.cfg.Build,
		Node: st.Node, HubPins: c.pins(), Verbs: c.cfg.Dispatcher.Info(),
	}
	if st.Node == "" || repair {
		if secret := c.pairingSecret(); secret != "" {
			hello.Node, hello.Pairing = "", secret
		}
	}
	if err := nodeproto.SendFrame(ctx, ch, nodeproto.OpHello, hello, nil); err != nil {
		ch.Close()
		return c.grow(), err
	}
	f, err := nodeproto.RecvFrame(ctx, ch, nodeproto.HandshakeTimeout)
	if err != nil {
		ch.Close()
		return c.grow(), err
	}
	switch f.Op {
	case nodeproto.OpWelcome:
	case nodeproto.OpRefused:
		ch.Close()
		return c.refused(rotating, f.Error)
	default:
		ch.Close()
		return c.grow(), fmt.Errorf("node: the hub answered %q to hello", f.Op)
	}
	var w nodeproto.Welcome
	if err := json.Unmarshal(f.Data, &w); err != nil || w.Node == "" {
		ch.Close()
		return c.grow(), errors.New("node: a malformed welcome")
	}
	if rotating {
		if err := c.cfg.Identity.CommitRotate(); err != nil {
			c.log.Error("finishing the key rotation failed", "err", err)
		} else {
			c.log.Info("rotated the node's key", "fingerprint", nodeproto.Fingerprint(id.Pin))
		}
	}
	if err := c.cfg.Identity.Update(func(s *State) { s.Node = w.Node }); err != nil {
		c.log.Error("saving the node ID failed", "err", err)
	}
	if w.HubPin != "" && !slices.Contains(c.pins(), w.HubPin) {
		c.takePin(w.HubPin)
	}
	c.mu.Lock()
	c.repair = false
	c.mu.Unlock()

	sess := nodeproto.NewSession(ch, c.sessionOptions())
	c.mu.Lock()
	c.sess = sess
	c.mu.Unlock()
	c.log.Info("connected to the hub", "node", w.Node, "protocol", ch.Version, "hub_fingerprint", nodeproto.Fingerprint(ch.PeerPin))
	c.cfg.Health.Set(StateConnected, w.Node)
	c.backoff = time.Second
	err = sess.Run(ctx)
	c.mu.Lock()
	c.sess = nil
	c.mu.Unlock()
	var pe *nodeproto.Error
	if errors.As(err, &pe) {
		return c.refused(false, pe)
	}
	return c.backoff, err
}

// pairingSecret is the secret to pair with: the pairing file's current
// one when it reads, else the one the node started with.
func (c *Client) pairingSecret() string {
	if c.cfg.PairingFile != "" {
		if b, err := os.ReadFile(c.cfg.PairingFile); err == nil {
			if secret, _, err := nodeproto.ParsePairing(string(b)); err == nil {
				c.mu.Lock()
				c.secret = secret
				c.mu.Unlock()
				return secret
			}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.secret
}

func (c *Client) grow() time.Duration {
	d := c.backoff
	c.backoff = min(c.backoff*2, 30*time.Second)
	return d
}

// refused handles a refusal: a forgotten node pairs again at once (when it
// can), a rotation the hub never saw is dropped, and anything else waits.
func (c *Client) refused(rotating bool, e *nodeproto.Error) (time.Duration, error) {
	if e == nil {
		e = nodeproto.Errorf(nodeproto.CodeProtocol, "refused without a reason")
	}
	if rotating && e.Code == nodeproto.CodeUnknownNode {
		c.log.Warn("the hub doesn't know the next key; keeping the current one")
		if err := c.cfg.Identity.AbortRotate(); err != nil {
			return c.grow(), err
		}
		return 0, nil
	}
	c.cfg.Health.Set(StateRefused, e.Code)
	c.mu.Lock()
	defer c.mu.Unlock()
	if e.Code == nodeproto.CodeUnknownNode && (c.secret != "" || c.cfg.PairingFile != "") && !c.repair {
		c.repair = true // pair again on the next attempt, right away
		return 0, fmt.Errorf("node: the hub doesn't know this node (%s); pairing again", e.Message)
	}
	wait := 30 * time.Second
	if e.Code == nodeproto.CodeRevoked || e.Code == nodeproto.CodeUnknownNode {
		wait = 5 * time.Minute
	}
	return wait, fmt.Errorf("node: the hub refused the node: %s (%s)", e.Message, e.Code)
}

func (c *Client) sessionOptions() nodeproto.SessionOptions {
	o := c.cfg.Session
	o.Handler = c.handle
	return o
}

// handle serves the hub's requests: the built-in hub.pin, then the modules.
func (c *Client) handle(ctx context.Context, method string, params json.RawMessage, emit func(json.RawMessage) error) (json.RawMessage, error) {
	if method == nodeproto.VerbHubPin {
		var m nodeproto.PinMsg
		if err := json.Unmarshal(params, &m); err != nil || !nodeproto.ValidPin(m.Pin) {
			return nil, Invalid("hub.pin needs a valid pin")
		}
		if !c.policy.PinRotation() {
			return nil, Forbidden("this node's policy fixes the hub's key; edit hub.pin to rotate it")
		}
		if err := c.cfg.Identity.AddHubPin(m.Pin); err != nil {
			return nil, err
		}
		c.log.Info("the hub pushed its next key", "fingerprint", nodeproto.Fingerprint(m.Pin))
		return json.RawMessage("{}"), nil
	}
	return c.cfg.Dispatcher.Call(ctx, method, params, func(b json.RawMessage) { _ = emit(b) })
}

func (c *Client) takePin(pin string) {
	if !c.policy.PinRotation() {
		return
	}
	if err := c.cfg.Identity.AddHubPin(pin); err != nil {
		c.log.Error("saving the hub's key failed", "err", err)
	}
}

// ErrNotConnected is RotateKey without a live session.
var ErrNotConnected = errors.New("node: not connected to the hub")

// RotateKey replaces the node's key over the live session: the node makes
// a next key, the hub records it, and the session is dropped so the node
// reconnects with it. The hub accepts both keys until it first sees the new
// one, so a crash in between leaves the node on its old key.
func (c *Client) RotateKey(ctx context.Context) error {
	c.mu.Lock()
	s := c.sess
	c.mu.Unlock()
	if s == nil {
		return ErrNotConnected
	}
	pin, err := c.cfg.Identity.BeginRotate()
	if err != nil {
		return err
	}
	if _, err := s.Call(ctx, nodeproto.VerbNodeRotate, nodeproto.RotateMsg{Next: pin}, nil); err != nil {
		_ = c.cfg.Identity.AbortRotate()
		return fmt.Errorf("node: the hub didn't take the next key: %w", err)
	}
	return s.Close()
}

// Connected reports whether a session is live.
func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sess != nil
}
