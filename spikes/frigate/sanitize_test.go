package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNamesAndAddresses(t *testing.T) {
	var cfg frigateConfig
	_ = json.Unmarshal([]byte(`{
		"cameras": {"driveway": {}, "gate_cam": {"zones": {"path_zone": {}}}},
		"go2rtc": {"streams": {"driveway": [], "gate_cam": [], "gate_cam_sub": [], "nursery": []}}
	}`), &cfg)
	n := buildNames(cfg)
	got := n.Replace(`{"gate_cam": "rtsp://127.0.0.1:8554/gate_cam_sub", "zone": "path_zone", "x": "ffmpeg:nursery#audio=opus", "y": "/clips/thumb-gate_cam-1.webp"}`)
	for _, real := range []string{"gate_cam", "path_zone", "nursery"} {
		if strings.Contains(got, real) {
			t.Errorf("real name %q survived: %s", real, got)
		}
	}
	if !strings.Contains(got, "side_deck_sub") || !strings.Contains(got, "walkway") || !strings.Contains(got, "stream_01") {
		t.Errorf("unexpected mapping: %s", got)
	}

	m := newMapper()
	m.nets["172.16.5"] = "10.0.11"
	a := m.Text("host 172.16.5.7 and 172.16.5.7")
	if a != "host 10.0.11.7 and 10.0.11.7" {
		t.Fatalf("Text = %q", a)
	}
	if b := m.Text(a); b != a {
		t.Fatalf("second pass remapped sanitized addresses: %q", b)
	}
}
