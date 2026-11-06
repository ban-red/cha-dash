package nut

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// Limits on what upsd may send, so a confused server can't grow memory.
const (
	maxLine     = 4096 // bytes per line
	maxUPS      = 64   // UPSes per server
	maxVars     = 1000 // variables per UPS
	dialTimeout = 10 * time.Second
	opTimeout   = 15 * time.Second // the whole session, when ctx has no deadline
)

// upsdError is an "ERR <code> [extra]" answer.
type upsdError struct {
	Cmd  string // the command's first word(s), never its arguments' values
	Code string
}

func (e *upsdError) Error() string { return fmt.Sprintf("upsd answered %s with ERR %s", e.Cmd, e.Code) }

// notUpsd is an answer that isn't NUT's protocol: something else on the port.
func notUpsd(cmd, why string) error {
	return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "port",
		Message: fmt.Sprintf("the server didn't answer %s like upsd (%s): check the host and port", cmd, why)}
}

// session is one TCP connection to upsd.
type session struct {
	conn net.Conn
	r    *bufio.Reader
	stop func() bool
}

// dial opens a session. The context bounds it: its deadline becomes the
// connection's, and cancelling it closes the connection.
func dial(ctx context.Context, addr string) (*session, error) {
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(opTimeout)
	}
	_ = conn.SetDeadline(deadline)
	s := &session{conn: conn, r: bufio.NewReaderSize(conn, maxLine)}
	s.stop = context.AfterFunc(ctx, func() { conn.Close() })
	return s, nil
}

// close says goodbye (best effort) and closes the connection.
func (s *session) close() {
	s.stop()
	_ = s.conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := s.conn.Write([]byte("LOGOUT\n")); err == nil {
		_, _, _ = s.r.ReadLine()
	}
	s.conn.Close()
}

// send writes one command line. name is what errors call it: never the
// password.
func (s *session) send(name string, words ...string) error {
	line := strings.Join(words, " ") + "\n"
	if _, err := s.conn.Write([]byte(line)); err != nil {
		return fmt.Errorf("sending %s: %w", name, err)
	}
	return nil
}

// line reads one line and splits it into words.
func (s *session) line(name string) ([]string, error) {
	b, more, err := s.r.ReadLine()
	if err != nil {
		if errors.Is(err, bufio.ErrBufferFull) || more {
			return nil, notUpsd(name, "a line longer than 4 KiB")
		}
		return nil, fmt.Errorf("reading the answer to %s: %w", name, err)
	}
	if more {
		return nil, notUpsd(name, "a line longer than 4 KiB")
	}
	words, ok := split(string(b))
	if !ok {
		return nil, notUpsd(name, "a line that isn't in its format")
	}
	if len(words) == 0 {
		return nil, notUpsd(name, "an empty line")
	}
	if words[0] == "ERR" {
		code := "UNKNOWN"
		if len(words) > 1 {
			code = words[1]
		}
		return nil, &upsdError{Cmd: name, Code: code}
	}
	return words, nil
}

// simple runs a command answered by one line starting with OK.
func (s *session) simple(name string, words ...string) error {
	if err := s.send(name, words...); err != nil {
		return err
	}
	ans, err := s.line(name)
	if err != nil {
		return err
	}
	if ans[0] != "OK" {
		return notUpsd(name, "no OK")
	}
	return nil
}

// auth sends USERNAME and PASSWORD. Stock upsd answers OK to both and checks
// them only for commands that change something; reads need neither. A
// server that does refuse them gives an auth error.
func (s *session) auth(user, pass string) error {
	if user == "" {
		return nil
	}
	if err := s.simple("USERNAME", "USERNAME", quote(user)); err != nil {
		return err
	}
	if pass == "" {
		return nil
	}
	return s.simple("PASSWORD", "PASSWORD", quote(pass))
}

// version runs VER: "Network UPS Tools upsd 2.8.2 - https://…". A server
// without VER gives "".
func (s *session) version() (string, error) {
	if err := s.send("VER", "VER"); err != nil {
		return "", err
	}
	b, more, err := s.r.ReadLine()
	if err != nil || more {
		if err == nil {
			return "", notUpsd("VER", "a line longer than 4 KiB")
		}
		return "", fmt.Errorf("reading the answer to VER: %w", err)
	}
	line := strings.TrimSpace(string(b))
	if strings.HasPrefix(line, "ERR ") {
		return "", nil // UNKNOWN-COMMAND on a very old upsd
	}
	if !strings.Contains(line, "upsd") && !strings.Contains(line, "Network UPS Tools") {
		return "", notUpsd("VER", "an answer that doesn't name upsd")
	}
	words := strings.Fields(line)
	for i, w := range words {
		if w == "upsd" && i+1 < len(words) {
			return words[i+1], nil
		}
	}
	return line, nil
}

// upsEntry is one LIST UPS line.
type upsEntry struct {
	Name string
	Desc string
}

// list runs LIST <what...> and returns the lines between BEGIN and END,
// each without its leading type word.
func (s *session) list(name string, limit int, what ...string) ([][]string, error) {
	if err := s.send(name, append([]string{"LIST"}, what...)...); err != nil {
		return nil, err
	}
	first, err := s.line(name)
	if err != nil {
		return nil, err
	}
	head := strings.Join(what, " ")
	if strings.Join(first, " ") != "BEGIN LIST "+head {
		return nil, notUpsd(name, "no BEGIN LIST")
	}
	var out [][]string
	for {
		w, err := s.line(name)
		if err != nil {
			return nil, err
		}
		if w[0] == "END" {
			if strings.Join(w, " ") != "END LIST "+head {
				return nil, notUpsd(name, "an END for another list")
			}
			return out, nil
		}
		if len(out) >= limit {
			return nil, notUpsd(name, "more than "+strconv.Itoa(limit)+" lines")
		}
		out = append(out, w)
	}
}

// listUPS runs LIST UPS.
func (s *session) listUPS() ([]upsEntry, error) {
	lines, err := s.list("LIST UPS", maxUPS, "UPS")
	if err != nil {
		return nil, err
	}
	out := make([]upsEntry, 0, len(lines))
	for _, w := range lines {
		if len(w) < 2 || w[0] != "UPS" {
			return nil, notUpsd("LIST UPS", "a line that isn't a UPS")
		}
		e := upsEntry{Name: w[1]}
		if len(w) > 2 && w[2] != "Unavailable" && w[2] != "Description unavailable" {
			e.Desc = w[2]
		}
		out = append(out, e)
	}
	return out, nil
}

// listVar runs LIST VAR <ups>.
func (s *session) listVar(ups string) (map[string]string, error) {
	lines, err := s.list("LIST VAR", maxVars, "VAR", ups)
	if err != nil {
		return nil, err
	}
	vars := make(map[string]string, len(lines))
	for _, w := range lines {
		if len(w) != 4 || w[0] != "VAR" || w[1] != ups {
			return nil, notUpsd("LIST VAR", "a line that isn't a variable")
		}
		vars[w[2]] = w[3]
	}
	return vars, nil
}

// split breaks a line into words by NUT's parser rules: whitespace
// separates, a double quote at the start of a word quotes it to the next
// unescaped quote, and a backslash escapes the next character. It reports
// false for an unterminated quote.
func split(line string) ([]string, bool) {
	var (
		out    []string
		cur    strings.Builder
		inWord bool
		quoted bool
	)
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\\':
			if i+1 < len(line) {
				i++
				cur.WriteByte(line[i])
				inWord = true
			}
		case quoted && c == '"':
			out = append(out, cur.String())
			cur.Reset()
			quoted, inWord = false, false
		case quoted:
			cur.WriteByte(c)
		case c == '"' && !inWord:
			quoted, inWord = true, true
		case c == ' ' || c == '\t' || c == '\r':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if quoted {
		return nil, false
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out, true
}

// quote makes one argument: in double quotes, with \ and " escaped.
func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
