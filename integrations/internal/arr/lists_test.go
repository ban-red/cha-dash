package arr

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestParseTimeLeft(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"00:12:30":   12*time.Minute + 30*time.Second,
		"1.02:00:00": 26 * time.Hour,
		"00:00:00":   0,
	} {
		if got, ok := parseTimeLeft(in); !ok || got != want {
			t.Errorf("%q = %v %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "soon", "1:2"} {
		if _, ok := parseTimeLeft(in); ok {
			t.Errorf("%q should not parse", in)
		}
	}
}

func TestQueueRecordShapes(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want QueueItem
	}{
		"movie": {`{"title":"Some.Movie.2026.1080p","status":"downloading","size":1000,"sizeleft":250,"timeleft":"00:05:00","protocol":"usenet","downloadClient":"SABnzbd","movie":{"title":"Some Movie","year":2026}}`,
			QueueItem{Title: "Some Movie", Detail: "2026", Status: "downloading", Progress: 75, LeftS: 300, Client: "SABnzbd", Protocol: "usenet"}},
		"album": {`{"title":"Artist-Album-WEB","status":"paused","size":100,"sizeleft":100,"artist":{"artistName":"The Artist"},"album":{"title":"The Album"}}`,
			QueueItem{Title: "The Artist", Detail: "The Album", Status: "paused"}},
		"unmatched release": {`{"title":"Some.Release.Name","status":"queued"}`, QueueItem{Title: "Some.Release.Name", Status: "queued"}},
		"failed": {`{"title":"x","status":"completed","trackedDownloadStatus":"error","errorMessage":"Download failed","movie":{"title":"M"}}`,
			QueueItem{Title: "M", Status: "error", Error: "Download failed"}},
	} {
		var r queueRecord
		if err := json.Unmarshal([]byte(tc.body), &r); err != nil {
			t.Fatal(err)
		}
		got, ok := r.item()
		if !ok || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %+v (%v), want %+v", name, got, ok, tc.want)
		}
	}
}
