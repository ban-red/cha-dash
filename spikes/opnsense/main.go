// Command opnsense-spike is throwaway code for plumbing spike 4.5: it reads an
// OPNsense router's API (GET only), records raw captures to a directory
// outside the repo, and turns them into sanitized fixtures.
//
//	go run . probe    --env ../../../.env.local --out $SCRATCH/opnsense-raw [--paths a,b]
//	go run . stream   --env ../../../.env.local --out $SCRATCH/opnsense-raw --path /api/diagnostics/traffic/stream/1 --window 30s
//	go run . shape    $SCRATCH/opnsense-raw/<file>.raw.json
//	go run . sanitize --raw $SCRATCH/opnsense-raw --out ../../testdata/integrations/opnsense
//	go run . needles  --raw $SCRATCH/opnsense-raw --env ../../../.env.local --out $SCRATCH/real.needles
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

func main() {
	log.SetFlags(log.Ltime)
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: opnsense-spike probe|stream|shape|sanitize|needles [flags]")
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "probe":
		err = runProbe(args)
	case "stream":
		err = runStream(args)
	case "shape":
		err = runShape(args)
	case "sanitize":
		err = runSanitize(args)
	case "needles":
		err = runNeedles(args)
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func newClientFromEnv(envPath, pinFile string) (*Client, error) {
	env, err := loadEnv(envPath)
	if err != nil {
		return nil, fmt.Errorf("env: %w", err)
	}
	if err := need(env, "OPNSENSE_URL", "OPNSENSE_KEY", "OPNSENSE_SECRET"); err != nil {
		return nil, err
	}
	return NewClient(env["OPNSENSE_URL"], env["OPNSENSE_KEY"], env["OPNSENSE_SECRET"], pinFile, 1500*time.Millisecond, 15*time.Second)
}

var nonSlug = regexp.MustCompile(`[^a-zA-Z0-9]+`)

func slug(path string) string {
	return strings.Trim(nonSlug.ReplaceAllString(path, "_"), "_")
}

func runProbe(args []string) error {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	envPath := fs.String("env", "", "dotenv file with OPNSENSE_URL, OPNSENSE_KEY, OPNSENSE_SECRET")
	out := fs.String("out", "", "directory for raw captures (outside the repo)")
	paths := fs.String("paths", "", "comma-separated API paths; default: the built-in catalogue")
	pin := fs.String("pin", "", "optional git-ignored file for the TOFU certificate pin")
	_ = fs.Parse(args)
	if *envPath == "" || *out == "" {
		return fmt.Errorf("probe: --env and --out are required")
	}
	c, err := newClientFromEnv(*envPath, *pin)
	if err != nil {
		return err
	}
	var list []Endpoint
	if *paths != "" {
		for _, p := range strings.Split(*paths, ",") {
			list = append(list, Endpoint{Path: strings.TrimSpace(p)})
		}
	} else {
		list = catalogue
	}
	if err := os.MkdirAll(*out, 0o700); err != nil {
		return err
	}
	index := map[string]Result{}
	idxPath := filepath.Join(*out, "index.raw.json")
	if b, err := os.ReadFile(idxPath); err == nil {
		_ = json.Unmarshal(b, &index)
	}
	for _, ep := range list {
		m := ep.Method
		if m == "" {
			m = "GET"
		}
		r := c.Do(m, ep.Path, 8<<20)
		log.Print(r.String())
		if len(r.Body) > 0 {
			if err := os.WriteFile(filepath.Join(*out, slug(ep.Path)+".raw.json"), r.Body, 0o600); err != nil {
				return err
			}
		}
		index[ep.Path] = r
	}
	log.Printf("tls mode: %s", c.TLSMode())
	b, _ := json.MarshalIndent(index, "", "  ")
	return os.WriteFile(idxPath, b, 0o600)
}

// streamEvent is one SSE event as received.
type streamEvent struct {
	AtMillis int64  `json:"at_ms"`
	Event    string `json:"event,omitempty"`
	ID       string `json:"id,omitempty"`
	Bytes    int    `json:"bytes"`
	Data     string `json:"data"`
}

func runStream(args []string) error {
	fs := flag.NewFlagSet("stream", flag.ExitOnError)
	envPath := fs.String("env", "", "dotenv file")
	out := fs.String("out", "", "directory for raw captures (outside the repo)")
	path := fs.String("path", "/api/diagnostics/traffic/stream/1", "stream path")
	window := fs.Duration("window", 30*time.Second, "how long to listen (capped at 55s)")
	pin := fs.String("pin", "", "optional TOFU pin file")
	_ = fs.Parse(args)
	if *envPath == "" || *out == "" {
		return fmt.Errorf("stream: --env and --out are required")
	}
	c, err := newClientFromEnv(*envPath, *pin)
	if err != nil {
		return err
	}
	var events []streamEvent
	var cur streamEvent
	var data []string
	var other []string
	res, err := c.Stream(*path, *window, func(line []byte, at time.Duration) bool {
		s := string(line)
		switch {
		case s == "":
			if len(data) > 0 || cur.Event != "" {
				cur.Data = strings.Join(data, "\n")
				cur.Bytes = len(cur.Data)
				events = append(events, cur)
			}
			cur, data = streamEvent{}, nil
		case strings.HasPrefix(s, "data:"):
			if cur.AtMillis == 0 {
				cur.AtMillis = at.Milliseconds()
			}
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(s, "data:"), " "))
		case strings.HasPrefix(s, "event:"):
			cur.Event = strings.TrimSpace(strings.TrimPrefix(s, "event:"))
			if cur.AtMillis == 0 {
				cur.AtMillis = at.Milliseconds()
			}
		case strings.HasPrefix(s, "id:"):
			cur.ID = strings.TrimSpace(strings.TrimPrefix(s, "id:"))
		case strings.HasPrefix(s, ":"):
			other = append(other, fmt.Sprintf("%dms comment len=%d", at.Milliseconds(), len(s)))
		default:
			if len(other) < 20 {
				other = append(other, fmt.Sprintf("%dms non-SSE line len=%d", at.Milliseconds(), len(s)))
			}
		}
		return true
	})
	if err != nil {
		log.Printf("stream error: %v", err)
	}
	log.Print(res.String())
	for _, o := range other {
		log.Print(o)
	}
	var prev int64
	for i, e := range events {
		log.Printf("event %2d at %6dms (+%5dms) event=%q bytes=%d", i, e.AtMillis, e.AtMillis-prev, e.Event, e.Bytes)
		prev = e.AtMillis
	}
	rec := map[string]any{"result": res, "events": events, "other": other, "window": window.String()}
	b, _ := json.MarshalIndent(rec, "", "  ")
	return os.WriteFile(filepath.Join(*out, "stream_"+slug(*path)+".raw.json"), b, 0o600)
}
