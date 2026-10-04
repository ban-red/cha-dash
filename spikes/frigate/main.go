// Command frigate-spike is throwaway code for plumbing spike 4.5: it reads a
// Frigate NVR's HTTP API and websocket (read-only), records scrubbed raw
// captures outside the repo, and turns them into sanitized fixtures.
//
// It never stores camera images, snapshots, clips, recordings or thumbnails:
// media endpoints are measured (status, type, size) and their bytes are
// discarded; base64 pictures inside JSON are removed before anything is
// written.
//
//	go run . probe    --env ../../../.env.local --out $SCRATCH/frigate-raw --secrets $SCRATCH/frigate.needles
//	go run . ws       --env ../../../.env.local --out $SCRATCH/frigate-raw --window 45s
//	go run . shape    $SCRATCH/frigate-raw/<file>.raw.json
//	go run . sanitize --raw $SCRATCH/frigate-raw --out ../../testdata/integrations/frigate
//	go run . needles  --raw $SCRATCH/frigate-raw --env ../../../.env.local --out $SCRATCH/frigate.needles
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

func main() {
	log.SetFlags(log.Ltime)
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: frigate-spike probe|ws|shape|sanitize|needles [flags]")
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "probe":
		err = runProbe(args)
	case "ws":
		err = runWS(args)
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

func baseURL(envPath string) (string, error) {
	env, err := loadEnv(envPath)
	if err != nil {
		return "", fmt.Errorf("env: %w", err)
	}
	if err := need(env, "FRIGATE_URL"); err != nil {
		return "", err
	}
	return env["FRIGATE_URL"], nil
}

var nonSlug = regexp.MustCompile(`[^a-zA-Z0-9]+`)

func slug(path string) string { return strings.Trim(nonSlug.ReplaceAllString(path, "_"), "_") }

type recorder struct {
	c     *Client
	out   string
	sec   *secrets
	index map[string]Result
	cams  []string
}

func (r *recorder) json(path string) Result {
	res := r.c.Do("GET", path, 16<<20)
	log.Print(res.String())
	if len(res.Body) > 0 {
		clean := scrubImages(res.Body, r.sec)
		res.Bytes = int64(len(res.Body)) // wire size, before scrubbing
		if err := os.WriteFile(filepath.Join(r.out, slug(path)+".raw.json"), clean, 0o600); err != nil {
			log.Printf("write: %v", err)
		}
	}
	res.Body = nil
	r.index[path] = res
	return res
}

func (r *recorder) media(method, path string, readCap int64) Result {
	res := r.c.Measure(method, path, readCap)
	shown := res
	for i, cam := range r.cams {
		shown.Path = strings.ReplaceAll(shown.Path, "/"+url.PathEscape(cam)+"/", fmt.Sprintf("/<camera#%d>/", i))
	}
	log.Print(shown.String())
	r.index[method+" "+path] = res
	return res
}

func runProbe(args []string) error {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	envPath := fs.String("env", "", "dotenv file with FRIGATE_URL")
	out := fs.String("out", "", "directory for scrubbed raw captures (outside the repo)")
	secPath := fs.String("secrets", "", "0600 needle file outside the repo for credentials found while scrubbing")
	media := fs.Bool("media", true, "measure snapshot/clip endpoints (size only, bytes discarded)")
	paths := fs.String("paths", "", "comma-separated metadata paths to fetch instead of the default set")
	mediaOnly := fs.Bool("media-only", false, "skip the metadata set; reuse the config and events already captured")
	_ = fs.Parse(args)
	if *envPath == "" || *out == "" {
		return fmt.Errorf("probe: --env and --out are required")
	}
	raw, err := baseURL(*envPath)
	if err != nil {
		return err
	}
	c, err := NewClient(raw, time.Second, 20*time.Second)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o700); err != nil {
		return err
	}
	r := &recorder{c: c, out: *out, sec: &secrets{}, index: map[string]Result{}}

	if *paths != "" {
		for _, p := range strings.Split(*paths, ",") {
			r.json(strings.TrimSpace(p))
		}
		return r.finish(*secPath)
	}

	if !*mediaOnly {
		r.json("/api/version")
		r.json("/api/profile")
		r.json("/api/config")
		r.json("/api/stats")
		r.json("/api/stats/history")
		r.json("/api/events?limit=25&include_thumbnails=0")
		r.json("/api/events/summary")
		r.json("/api/review?limit=25")
		r.json("/api/review/summary")
		r.json("/api/recordings/storage")
		r.json("/api/labels")
		r.json("/api/sub_labels")
		r.json("/api/timeline?limit=25")
	}

	cams := cameraNames(filepath.Join(*out, "api_config.raw.json"))
	r.cams = cams
	log.Printf("%d cameras in config", len(cams))

	if *media {
		for _, cam := range cams {
			p := "/api/" + url.PathEscape(cam) + "/latest.jpg"
			r.media("GET", p, 8<<20)
		}
		if len(cams) > 0 {
			p := "/api/" + url.PathEscape(cams[0])
			r.media("GET", p+"/latest.jpg?height=180", 8<<20)
			r.media("GET", p+"/latest.webp?height=180", 8<<20)
			r.media("HEAD", p+"/latest.jpg", 1)
		}
		if id := recentEventID(filepath.Join(*out, "api_events_limit_25_include_thumbnails_0.raw.json")); id != "" {
			e := "/api/events/" + url.PathEscape(id)
			r.media("GET", e+"/snapshot.jpg", 8<<20)
			r.media("GET", e+"/snapshot.jpg?crop=1&height=180", 8<<20)
			r.media("GET", e+"/thumbnail.jpg", 8<<20)
			r.media("GET", e+"/thumbnail.webp", 8<<20)
			r.media("HEAD", e+"/clip.mp4", 1)
			r.media("GET", e+"/clip.mp4", 16<<20)
		}
	}

	log.Printf("tls mode: %s", c.TLSMode())
	return r.finish(*secPath)
}

func (r *recorder) finish(secPath string) error {
	if err := r.sec.save(secPath); err != nil {
		return err
	}
	idx := filepath.Join(r.out, "index.raw.json")
	old := map[string]Result{}
	if b, err := os.ReadFile(idx); err == nil {
		_ = json.Unmarshal(b, &old)
	}
	for k, v := range r.index {
		old[k] = v
	}
	b, _ := json.MarshalIndent(old, "", "  ")
	return os.WriteFile(idx, b, 0o600)
}

// cameraNames reads camera keys from the scrubbed config, in memory only.
func cameraNames(path string) []string {
	var cfg struct {
		Cameras map[string]json.RawMessage `json:"cameras"`
	}
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &cfg) != nil {
		return nil
	}
	names := make([]string, 0, len(cfg.Cameras))
	for k := range cfg.Cameras {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// recentEventID picks the newest event that has a snapshot and a clip.
func recentEventID(path string) string {
	var evs []struct {
		ID          string `json:"id"`
		HasSnapshot bool   `json:"has_snapshot"`
		HasClip     bool   `json:"has_clip"`
	}
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &evs) != nil {
		return ""
	}
	for _, e := range evs {
		if e.HasSnapshot && e.HasClip {
			return e.ID
		}
	}
	if len(evs) > 0 {
		return evs[0].ID
	}
	return ""
}
