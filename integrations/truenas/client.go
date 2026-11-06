package truenas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"dash.cha.sh/internal/integration"
)

// The WebSocket JSON-RPC 2.0 API, TrueNAS 25.04 and later: one connection
// to /api/current, authenticated once with auth.login_with_api_key (the
// key in the message, never the URL), then one call at a time. The
// connection stays open between syncs, so a sync each minute isn't a
// login each minute in TrueNAS's audit log.

const (
	apiPath   = "/api/current"
	readLimit = 16 << 20 // pool.dataset.query on a big pool runs to megabytes
)

// JSON-RPC and middleware error codes.
const (
	rpcMethodNotFound = -32601
	rpcCallError      = -32001
	errnoEACCES       = 13
)

// rpcError is a JSON-RPC error object. data carries the middleware's
// errno, errname and reason; its trace is never kept.
type rpcError struct {
	Method  string `json:"-"`
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Error   int    `json:"error"`
		Errname string `json:"errname"`
		Reason  string `json:"reason"`
	} `json:"data"`
}

func (e *rpcError) Error() string {
	msg := strings.TrimSpace(e.Data.Reason)
	if msg == "" {
		msg = strings.TrimSpace(e.Message)
	}
	if msg == "" {
		msg = "error " + strconv.Itoa(e.Code)
	}
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return e.Method + ": " + msg
}

// missing: the method isn't in this release's API.
func (e *rpcError) missing() bool {
	return e.Code == rpcMethodNotFound || e.Data.Errname == "ENOMETHOD"
}

// denied: the key's user may not call it.
func (e *rpcError) denied() bool {
	return e.Data.Error == errnoEACCES || e.Data.Errname == "EACCES" || e.Data.Errname == "EPERM" ||
		e.Data.Errname == "ENOTAUTHENTICATED"
}

func rpcErr(err error) *rpcError {
	if re, ok := errors.AsType[*rpcError](err); ok {
		return re
	}
	return nil
}

// conn is one authenticated WebSocket session.
type conn struct {
	ws   *websocket.Conn
	next int
}

// client holds the session across syncs. Calls run one at a time.
type client struct {
	http *http.Client
	url  string // wss://host[:port]/api/current
	key  string

	mu sync.Mutex
	c  *conn
}

func (cl *client) close() {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.c != nil {
		cl.c.ws.Close(websocket.StatusNormalClosure, "")
		cl.c = nil
	}
}

// dial opens and authenticates a session.
func (cl *client) dial(ctx context.Context) (*conn, error) {
	ws, resp, err := websocket.Dial(ctx, cl.url, &websocket.DialOptions{HTTPClient: cl.http})
	if err != nil {
		if resp != nil {
			switch resp.StatusCode {
			case http.StatusNotFound:
				return nil, &integration.RepairError{Code: integration.CodeUnsupportedVersion,
					Message: "TrueNAS has no " + apiPath + " WebSocket API: Cha Dash reads TrueNAS 25.04 and later"}
			case http.StatusUnauthorized, http.StatusForbidden:
				return nil, &integration.StatusError{Method: "GET", Path: apiPath, Code: resp.StatusCode}
			}
			if resp.StatusCode != http.StatusSwitchingProtocols && resp.StatusCode != 0 {
				return nil, &integration.StatusError{Method: "GET", Path: apiPath, Code: resp.StatusCode}
			}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("opening TrueNAS's WebSocket API: %w", scrub(err, cl.key))
	}
	ws.SetReadLimit(readLimit)
	c := &conn{ws: ws}
	var ok bool
	if err := c.call(ctx, "auth.login_with_api_key", []any{cl.key}, &ok); err != nil {
		ws.CloseNow()
		if re := rpcErr(err); re != nil && re.denied() {
			return nil, &integration.AuthError{Field: "api_key", Message: "TrueNAS refused the API key"}
		}
		return nil, scrub(err, cl.key)
	}
	if !ok {
		ws.CloseNow()
		// False covers a wrong key, an expired or revoked one, and a key
		// TrueNAS revoked because it arrived over plain HTTP.
		return nil, &integration.AuthError{Field: "api_key", Message: "TrueNAS rejected the API key: it's wrong, expired or revoked"}
	}
	return c, nil
}

// session returns the open session, dialling one when there's none.
// fresh reports whether it was just dialled.
func (cl *client) session(ctx context.Context) (c *conn, fresh bool, err error) {
	if cl.c != nil {
		return cl.c, false, nil
	}
	c, err = cl.dial(ctx)
	if err != nil {
		return nil, false, err
	}
	cl.c = c
	return c, true, nil
}

// call runs one method. A session left open since the last sync may have
// been closed by TrueNAS (a restart, an idle timeout): when a call on a
// reused session fails in transport, the session is dialled again once.
// That's reconnecting, not retrying a failure: a call that fails on a
// fresh session fails the sync.
func (cl *client) call(ctx context.Context, method string, params []any, dst any) error {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	return cl.clean(cl.callLocked(ctx, method, params, dst))
}

func (cl *client) callLocked(ctx context.Context, method string, params []any, dst any) error {
	c, fresh, err := cl.session(ctx)
	if err != nil {
		return err
	}
	err = c.call(ctx, method, params, dst)
	if err == nil || rpcErr(err) != nil || ctx.Err() != nil {
		return err
	}
	c.ws.CloseNow()
	cl.c = nil
	if fresh {
		return err
	}
	if c, _, err = cl.session(ctx); err != nil {
		return err
	}
	if err = c.call(ctx, method, params, dst); err != nil && rpcErr(err) == nil {
		c.ws.CloseNow()
		cl.c = nil
	}
	return err
}

// clean keeps the key out of an error: a JSON-RPC error's text is
// redacted in place, so it stays an *rpcError; anything else is rebuilt.
func (cl *client) clean(err error) error {
	if re := rpcErr(err); re != nil && cl.key != "" {
		re.Message = strings.ReplaceAll(re.Message, cl.key, "[redacted]")
		re.Data.Reason = strings.ReplaceAll(re.Data.Reason, cl.key, "[redacted]")
		return err
	}
	return scrub(err, cl.key)
}

type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id"`
	Method  string          `json:"method"` // a notification
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

func (c *conn) call(ctx context.Context, method string, params []any, dst any) error {
	if params == nil {
		params = []any{}
	}
	c.next++
	id := c.next
	b, err := json.Marshal(request{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return err
	}
	if err := c.ws.Write(ctx, websocket.MessageText, b); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	for {
		typ, data, err := c.ws.Read(ctx)
		if err != nil {
			return fmt.Errorf("%s: %w", method, err)
		}
		if typ != websocket.MessageText {
			continue
		}
		var r response
		if err := json.Unmarshal(data, &r); err != nil {
			return fmt.Errorf("%s: the answer isn't JSON-RPC: %w", method, err)
		}
		if r.ID == nil || *r.ID != id {
			continue // a notification, or an answer to a call given up on
		}
		if r.Error != nil {
			r.Error.Method = method
			return r.Error
		}
		if dst == nil {
			return nil
		}
		if err := json.Unmarshal(r.Result, dst); err != nil {
			return fmt.Errorf("%s: unexpected shape: %w", method, err)
		}
		return nil
	}
}

// scrub keeps the key out of an error that might quote what was sent.
func scrub(err error, key string) error {
	if err == nil || key == "" || !strings.Contains(err.Error(), key) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), key, "[redacted]"))
}

// ── values ──────────────────────────────────────────────────────────────

// tnTime is a middleware datetime: {"$date": <Unix ms>} on the wire, or
// (defensively) a number of ms or an RFC 3339 string.
type tnTime struct{ time.Time }

func (t *tnTime) UnmarshalJSON(b []byte) error {
	t.Time = time.Time{}
	s := strings.TrimSpace(string(b))
	switch {
	case s == "null" || s == "":
		return nil
	case strings.HasPrefix(s, "{"):
		var v struct {
			Date *float64 `json:"$date"`
		}
		if json.Unmarshal(b, &v) == nil && v.Date != nil {
			t.Time = time.UnixMilli(int64(*v.Date)).UTC()
		}
	case strings.HasPrefix(s, `"`):
		var str string
		if json.Unmarshal(b, &str) == nil {
			if p, err := time.Parse(time.RFC3339, str); err == nil {
				t.Time = p.UTC()
			}
		}
	default:
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			t.Time = time.UnixMilli(int64(f)).UTC()
		}
	}
	return nil
}

// strSet is a list of strings, sent as a list or as the middleware's
// {"$set": [...]}.
type strSet []string

func (s *strSet) UnmarshalJSON(b []byte) error {
	var list []string
	if json.Unmarshal(b, &list) == nil {
		*s = list
		return nil
	}
	var set struct {
		Set []string `json:"$set"`
	}
	if json.Unmarshal(b, &set) == nil {
		*s = set.Set
		return nil
	}
	*s = nil
	return nil
}

// num is a number that also takes numeric strings and null.
type num struct {
	v  float64
	ok bool
}

func (n *num) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	f, err := strconv.ParseFloat(s, 64)
	*n = num{f, err == nil}
	return nil
}
