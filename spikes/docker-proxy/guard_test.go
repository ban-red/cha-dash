package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"dash.cha.sh/spikes/traefik/labkit"
)

func TestGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer srv.Close()
	c := labkit.NewReadOnlyClient(nil, dockerGuard, 0, 5*time.Second)
	id := "0123456789ab"
	allow := []string{
		"/_ping", "/v1.47/version", "/info", "/v1.47/containers/json?all=1",
		"/v1.47/containers/" + id + "/json", "/containers/" + id + "/stats?stream=false",
		"/containers/" + id + "/logs?stdout=0&stderr=0&tail=0",
		"/events?since=1&until=2", "/images/json", "/images/lscr.io/linuxserver/sonarr:latest/json",
		"/networks", "/networks/bridge", "/volumes", "/system/df?type=image", "/secrets", "/swarm",
	}
	deny := []string{
		"/containers/" + id + "/exec", "/exec/abc/json", "/containers/" + id + "/attach",
		"/containers/" + id + "/logs?stdout=1&stderr=0", "/containers/" + id + "/logs?stdout=0&stderr=0&follow=1",
		"/containers/" + id + "/archive?path=/", "/containers/" + id + "/export",
		"/containers/" + id + "/top", "/events", "/events?since=1", "/containers/" + id + "/stats",
		"/images/create", "/build", "/v1.47/containers/../../info", "/distribution/x/json",
	}
	for _, p := range allow {
		resp, err := c.Get(srv.URL + p)
		if err != nil {
			t.Errorf("allow %s: %v", p, err)
			continue
		}
		resp.Body.Close()
	}
	for _, p := range deny {
		if _, err := c.Get(srv.URL + p); !errors.Is(err, labkit.ErrRefused) {
			t.Errorf("deny %s: err = %v, want refused", p, err)
		}
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/containers/"+id+"/restart", nil)
	if _, err := c.Do(req); !errors.Is(err, labkit.ErrRefused) {
		t.Errorf("POST restart: err = %v, want refused", err)
	}
}
