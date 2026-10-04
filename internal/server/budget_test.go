package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/boards"
	"dash.cha.sh/internal/demo"
	"dash.cha.sh/internal/firstpaint"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/topics"
	"dash.cha.sh/internal/webui"
)

// First-paint budgets (03 · Architecture → Performance budgets: first
// meaningful paint on a LAN ≤ 300 ms, with real last-known values). These
// are the hub's share, measured on the Demo Home: CI enforces them in a run
// without -race (see .github/workflows/ci.yml, "Performance budgets").
const (
	// Time to answer GET / for a signed-in owner: session lookup, the
	// first board and its layout, the snapshot, encoding and the page.
	budgetFirstPaintMedian = 10 * time.Millisecond
	budgetFirstPaintP95    = 50 * time.Millisecond // shared CI runners have noisy tails
	// The inline snapshot over the Demo Home's afternoon and evening, the
	// busiest stretch of its day (decimal kB, as served: not compressed).
	budgetSnapshotBytes = 200_000
)

type budgetClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *budgetClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *budgetClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// demoLayout is a board the size of the Demo Home's default one: four
// sections, sixteen tiles. The hub stores layouts as opaque JSON.
func demoLayout() json.RawMessage {
	type item struct {
		ID             string   `json:"id"`
		Type           string   `json:"type"`
		Size           string   `json:"size"`
		SupportedSizes []string `json:"supportedSizes"`
		X              int      `json:"x"`
		Y              int      `json:"y"`
	}
	type section struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Span  int    `json:"span"`
		Items []item `json:"items"`
	}
	doc := struct {
		Version  int       `json:"version"`
		Columns  int       `json:"columns"`
		Sections []section `json:"sections"`
	}{Version: 1, Columns: 12}
	for s, title := range []string{"Home", "Infrastructure", "Media", "Activity"} {
		sec := section{ID: fmt.Sprintf("s%d", s), Title: title, Span: 4 + 4*(s%2)}
		for i := range 4 {
			sec.Items = append(sec.Items, item{ID: fmt.Sprintf("host@pve:node/%d-%d", s, i), Type: "host", Size: "wide", SupportedSizes: []string{"small", "wide"}, X: i % 2 * 2, Y: i / 2})
		}
		doc.Sections = append(doc.Sections, sec)
	}
	b, _ := json.Marshal(doc)
	return b
}

// TestBudgetFirstPaint times GET / for a signed-in owner on the Demo Home,
// in process (the handler alone, no network), and sizes its inline snapshot
// through the afternoon and evening of the demo day, incidents included.
func TestBudgetFirstPaint(t *testing.T) {
	if raceEnabled {
		t.Skip("a timing budget: CI runs it without -race")
	}
	clock := &budgetClock{t: time.Date(2031, 5, 6, 7, 8, 9, 0, time.UTC)}
	var drv *demo.Driver
	e := newEnvWith(t, func(d *Deps) {
		tp := topics.New(graph.New(), clock.Now())
		var err error
		drv, err = demo.New(tp, demo.Options{Seed: 1, At: 13 * time.Hour, Clock: clock.Now})
		if err != nil {
			t.Fatal(err)
		}
		d.Demo = drv
		d.Web = webui.Handler(webFS())
		d.FirstPaint = &firstpaint.Builder{Topics: tp, Boards: boards.New(d.Store), Demo: true}
	})
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	if resp := e.request(t, c, "POST", "/api/setup", map[string]string{"token": e.token, "username": "owner", "password": "correct horse battery"}, nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d", resp.StatusCode)
	}
	var list struct{ Boards []boards.Board }
	resp := e.request(t, c, "GET", "/api/boards", nil, nil)
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil || len(list.Boards) == 0 {
		t.Fatalf("boards: %v %+v", err, list)
	}
	body := map[string]any{"revision": 0, "format_version": 1, "layout": demoLayout()}
	if resp := e.request(t, c, "PUT", "/api/boards/"+list.Boards[0].ID+"/layout", body, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("save layout: %d", resp.StatusCode)
	}

	h := e.srv.Config.Handler
	u, _ := url.Parse(e.srv.URL)
	cookies := jar.Cookies(u)
	get := func() (time.Duration, int) {
		req := httptest.NewRequest("GET", "/", nil)
		for _, ck := range cookies {
			req.AddCookie(ck)
		}
		rec := httptest.NewRecorder()
		start := time.Now()
		h.ServeHTTP(rec, req)
		took := time.Since(start)
		m := dataBlock.FindSubmatch(rec.Body.Bytes())
		if rec.Code != 200 || m == nil {
			t.Fatalf("GET /: %d, no snapshot", rec.Code)
		}
		return took, len(m[1])
	}

	// Size over 13:00–20:30, sampled every simulated half hour.
	largest, at := 0, ""
	for tick := 0; tick <= 900; tick++ {
		if tick%60 == 0 {
			if _, n := get(); n > largest {
				largest, at = n, drv.Now().Format("15:04")
			}
		}
		clock.Advance(30 * time.Second)
		drv.Step()
	}

	// Time, at 20:30 with the day's incidents behind it.
	for range 10 {
		get() // warm up
	}
	runtime.GC() // start from a clean heap, not the sweep's garbage
	var took []time.Duration
	for range 100 {
		d, _ := get()
		took = append(took, d)
	}
	slices.Sort(took)
	median, p95 := took[len(took)/2], took[len(took)*95/100]
	t.Logf("first paint: GET / median %s, p95 %s (budgets %s, %s); largest inline snapshot %.1f kB at %s (budget %.0f kB, cap %.0f kB)",
		median.Round(10*time.Microsecond), p95.Round(10*time.Microsecond), budgetFirstPaintMedian, budgetFirstPaintP95,
		float64(largest)/1000, at, float64(budgetSnapshotBytes)/1000, float64(firstpaint.MaxBytes)/1000)

	if largest > budgetSnapshotBytes {
		t.Errorf("the inline snapshot reached %.1f kB at %s, over the %.0f kB budget", float64(largest)/1000, at, float64(budgetSnapshotBytes)/1000)
	}
	if median > budgetFirstPaintMedian || p95 > budgetFirstPaintP95 {
		t.Errorf("GET / took %s median, %s p95: over the %s / %s budget", median, p95, budgetFirstPaintMedian, budgetFirstPaintP95)
	}
}
