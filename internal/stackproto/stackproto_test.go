package stackproto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// TestRemoteAuthRedacted: a git credential never prints, whatever the
// formatting path, alone or inside a request.
func TestRemoteAuthRedacted(t *testing.T) {
	const secret = "ghp_FAKEexampleTOKEN0123456789abcdef"
	a := RemoteAuth{Kind: AuthHTTPS, Username: "sam", Secret: secret}
	fetch := FetchRequest{URL: "https://git.lab.example/sam/stacks.git", Branch: "main", Path: "app/compose.yaml", Auth: a}
	check := &RemoteCheckRequest{URL: fetch.URL, Branch: "main", Auth: a}

	var out []string
	for _, f := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
		out = append(out, fmt.Sprintf(f, a), fmt.Sprintf(f, fetch), fmt.Sprintf(f, check))
	}
	out = append(out, a.String(), fmt.Sprint(a), fmt.Sprintln(fetch))
	var buf bytes.Buffer
	for _, h := range []slog.Handler{slog.NewTextHandler(&buf, nil), slog.NewJSONHandler(&buf, nil)} {
		slog.New(h).Info("sync", "auth", a, slog.Any("ptr", &a))
	}
	out = append(out, buf.String())
	for _, s := range out {
		if strings.Contains(s, secret) || strings.Contains(s, "FAKEexample") {
			t.Fatalf("the secret printed: %s", s)
		}
	}
	if !strings.Contains(a.String(), "sam") || !strings.Contains(a.String(), "redacted") {
		t.Fatalf("String = %s", a)
	}

	// The wire still carries it: the node needs it.
	b, _ := json.Marshal(fetch)
	var back FetchRequest
	if err := json.Unmarshal(b, &back); err != nil || back.Auth.Secret != secret {
		t.Fatalf("wire round trip: %v %+v", err, back.Auth)
	}
}

func TestForbiddenSurvivesTheTransport(t *testing.T) {
	err := fmt.Errorf("node local: %w", errors.New(Errorf(CodeForbidden, "git.other.example isn't in stacks.remotes").Error()))
	if e := ErrorFrom(err); e == nil || e.Code != CodeForbidden {
		t.Fatalf("ErrorFrom = %v", e)
	}
}

func TestNewFieldsOnTheWire(t *testing.T) {
	for _, c := range []struct {
		v    any
		want string
	}{
		{DeployRequest{Stack: "app", Source: "https://git.lab.example/x.git@abc123"}, `"source":"https://git.lab.example/x.git@abc123"`},
		{Detected{Name: "app", MoveRefusal: "a root directory app already exists"}, `"moveable":false,"move_refusal":"a root directory app already exists"`},
		{MoveResult{Rewrites: []Rewrite{{Line: 7, From: "./data", To: "/srv/app/data"}}}, `"rewrites":[{"line":7,"from":"./data","to":"/srv/app/data"}]`},
		{SelfUpdateResult{Outcome: OutcomeDeployed, Previous: "sha256:a", Current: "sha256:b"}, `{"outcome":"deployed","previous":"sha256:a","current":"sha256:b"}`},
	} {
		b, err := json.Marshal(c.v)
		if err != nil || !strings.Contains(string(b), c.want) {
			t.Errorf("%T = %s %v, want %s", c.v, b, err, c.want)
		}
	}
	if b, _ := json.Marshal(DeployRequest{Stack: "app"}); strings.Contains(string(b), "source") {
		t.Errorf("an empty source is sent: %s", b)
	}
}
