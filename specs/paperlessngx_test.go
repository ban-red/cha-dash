package specs

import (
	"net/http"
	"strings"
	"testing"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const paperlessToken = "fake-paperless-token-0123456789abcdef"

func paperless(t *testing.T) *app {
	return &app{t: t, dir: "testdata/paperlessngx", base: "/paperless",
		auth:   func(r *http.Request) bool { return r.Header.Get("Authorization") == "Token "+paperlessToken },
		routes: map[string]string{"/api/statistics/": "statistics.json", "/api/status/": "status.json"}}
}

func TestPaperlessNgx(t *testing.T) {
	integ := bundled(t, "paperlessngx", "Paperless-ngx", "token")
	importerShape(t, integ, "https://paperless.home.arpa", "token")
	up := paperless(t)
	srv := up.start()
	url := srv.URL + "/paperless"
	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": url}, map[string]string{"token": paperlessToken})

	if res := inst.Test(t.Context()); !res.OK || res.Version != "2.17.1" {
		t.Fatalf("test = %+v", res)
	}
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	attrs := checkApp(t, h, "paperlessngx", url)
	r, _ := h.Resource("paperlessngx:i_test/app")
	if r.Health != integration.HealthOK {
		t.Fatalf("health = %+v", r)
	}
	if attrs["version"] != "2.17.1" || attrs["tags"] != 43.0 || attrs["correspondents"] != 118.0 || attrs["document_types"] != 21.0 || attrs["storage_paths"] != 4.0 {
		t.Fatalf("attrs = %v", attrs)
	}
	checkSeries(t, h, "paperlessngx", map[string]any{"documents": 2731.0, "inbox": 7.0}, nil)
	for _, req := range up.requests() {
		if strings.Contains(req.URL.String(), paperlessToken) {
			t.Fatal("the token is in a URL")
		}
	}

	// A view-only user can't read /api/status/: the version is unknown,
	// everything else reads.
	up.set("/api/status/", "")
	if res := inst.Test(t.Context()); !res.OK || res.Version != "" {
		t.Fatalf("no status: %+v", res)
	}
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	checkSeries(t, h, "paperlessngx", map[string]any{"documents": 2731.0}, nil)

	// No inbox tag: the inbox is unknown, not 0.
	up.set("/api/statistics/", `{"documents_total": 12, "documents_inbox": null, "inbox_tag": null}`)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	checkSeries(t, h, "paperlessngx", map[string]any{"documents": 12.0, "inbox": nil}, nil)
}

func TestPaperlessNgxInboxThreshold(t *testing.T) {
	integ := bundled(t, "paperlessngx", "Paperless-ngx", "token")
	srv := paperless(t).start()
	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": srv.URL + "/paperless", "inbox_warn": 5}, map[string]string{"token": paperlessToken})
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	r, _ := h.Resource("paperlessngx:i_test/app")
	if r.Health != integration.HealthAttention || r.Attrs["health.reason"] != "7 documents are waiting in the inbox" {
		t.Fatalf("over the threshold: %+v", r)
	}
}

func TestPaperlessNgxFailures(t *testing.T) {
	integ := bundled(t, "paperlessngx", "Paperless-ngx", "token")
	up := paperless(t)
	srv := up.start()
	res := integrationtest.New(t).Connect(integ, map[string]any{"url": srv.URL + "/paperless"}, map[string]string{"token": "wrong-token"}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth || strings.Contains(res.ErrorMessage, "wrong-token") {
		t.Fatalf("wrong token: %+v", res)
	}
	res = integrationtest.New(t).Connect(integ, map[string]any{"url": srv.URL + "/elsewhere"}, map[string]string{"token": paperlessToken}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeUnreachable {
		t.Fatalf("wrong url: %+v", res)
	}
}
