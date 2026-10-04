package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// fakeRT records requests and answers without any network or listener.
type fakeRT struct {
	reqs []*http.Request
	resp func(*http.Request) (*http.Response, error)
}

func (f *fakeRT) RoundTrip(r *http.Request) (*http.Response, error) {
	f.reqs = append(f.reqs, r)
	if f.resp != nil {
		return f.resp(r)
	}
	return jsonResp(200, `{"data":{"online":true}}`), nil
}

func jsonResp(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func testClient(t *testing.T, rt http.RoundTripper, key string) *Client {
	t.Helper()
	red := NewRedactor()
	red.AddURL(testURL)
	red.Add(key, "<api-key>")
	c, err := newClient(instanceConfig{Name: "TEST", URL: testURL, APIKey: key}, red, 5*time.Second, 0, rt)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestMutationNeverReachesTheWire(t *testing.T) {
	rt := &fakeRT{}
	c := testClient(t, rt, testKey)
	_, err := c.Query(context.Background(), "M", `mutation M { archiveAll { unread { total } } }`, nil)
	if !errors.Is(err, ErrMutation) {
		t.Fatalf("want ErrMutation, got %v", err)
	}
	if len(rt.reqs) != 0 {
		t.Fatalf("a refused mutation still made %d request(s)", len(rt.reqs))
	}
}

func TestMutationSubscriptionRefusedBeforeDial(t *testing.T) {
	rt := &fakeRT{}
	c := testClient(t, rt, testKey)
	rep, err := c.Subscribe(context.Background(), []Sub{
		{"M", `mutation M { archiveAll { unread { total } } }`},
		{"Q", `query Q { online }`},
	}, 5*time.Second)
	if err == nil {
		t.Fatal("want an error when every subscription is refused")
	}
	if len(rep.Subs) != 2 || !strings.Contains(rep.Subs[0].Refused, "mutation") || rep.Subs[1].Refused == "" {
		t.Fatalf("want both reported as refused, got %+v", rep.Subs)
	}
	if len(rt.reqs) != 0 {
		t.Fatalf("the socket was dialled (%d request(s)) although nothing may be sent", len(rt.reqs))
	}
}

func TestKeyOnlyOnPOST(t *testing.T) {
	rt := &fakeRT{}
	c := testClient(t, rt, testKey)
	if _, err := c.Query(context.Background(), "Q", `query Q { online }`, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(context.Background(), "/", "text/html"); err != nil {
		t.Fatal(err)
	}
	if got := rt.reqs[0].Header.Get("x-api-key"); got != testKey {
		t.Errorf("POST should carry the key")
	}
	if got := rt.reqs[1].Header.Get("x-api-key"); got != "" {
		t.Errorf("GET must not carry the key")
	}
	if rt.reqs[0].URL.Path != "/graphql" || rt.reqs[0].Method != http.MethodPost {
		t.Errorf("unexpected request %s %s", rt.reqs[0].Method, rt.reqs[0].URL.Path)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	rt := &fakeRT{resp: func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"https://abc123.myunraid.net/graphql"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	}}
	c := testClient(t, rt, testKey)
	res, err := c.Query(context.Background(), "Q", `query Q { online }`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 302 || len(rt.reqs) != 1 {
		t.Fatalf("redirect was followed: status %d after %d request(s)", res.Status, len(rt.reqs))
	}
	if d := c.describeLocation(res.Location); strings.Contains(d, "myunraid") || !strings.Contains(d, "another host") {
		t.Errorf("describeLocation should hide the host: %q", d)
	}
}

func TestTransportErrorsAreRedacted(t *testing.T) {
	rt := &fakeRT{resp: func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp 192.168.77.14:80: connect: no route to host")
	}}
	c := testClient(t, rt, testKey)
	_, err := c.Query(context.Background(), "Q", `query Q { online }`, nil)
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "192.168.77.14") {
		t.Fatalf("error leaks the address: %v", err)
	}
}

func TestResultCodes(t *testing.T) {
	rt := &fakeRT{resp: func(r *http.Request) (*http.Response, error) {
		return jsonResp(200, `{"data":{"upsDevices":null},"errors":[{"message":"Forbidden resource","extensions":{"code":"FORBIDDEN"}},{"message":"x"}]}`), nil
	}}
	c := testClient(t, rt, testKey)
	res, err := c.Query(context.Background(), "Q", `query Q { upsDevices { id } }`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(res.codes(), ","); got != "FORBIDDEN,NO_CODE" {
		t.Errorf("codes: %q", got)
	}
	if hasData(res) {
		t.Error("a null field should not count as data")
	}
}
