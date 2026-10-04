package integration

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

type authFake struct{ fake }

func (authFake) BeginAuth(context.Context, AuthDeps) (Authorization, error) {
	return Authorization{}, nil
}

func (authFake) PollAuth(context.Context, AuthDeps, Authorization) (map[string]string, bool, error) {
	return nil, false, nil
}

func authManifest(id string) Manifest {
	m := manifest(id)
	tok := Secret("Token")
	tok.Authorize = true
	m.ConfigSchema = Object(URLProp(), TLSProp(), Prop("token", tok)).Require("url", "token")
	return m
}

func TestAuthorizeMark(t *testing.T) {
	m := authManifest("plex")
	if err := ValidateManifest(m); err != nil {
		t.Fatal(err)
	}
	if got := m.AuthorizeFields(); !reflect.DeepEqual(got, []string{"token"}) {
		t.Fatalf("authorize fields = %v", got)
	}
	b, _ := json.Marshal(m.ConfigSchema)
	if !strings.Contains(string(b), `"token":{"type":"string","title":"Token","minLength":1,"maxLength":8192,"writeOnly":true,"x-chadash-authorize":true}`) {
		t.Fatalf("schema = %s", b)
	}
	// Only on a secret.
	bad := manifest("x")
	plain := String("Username")
	plain.Authorize = true
	bad.ConfigSchema = Object(Prop("username", plain))
	if err := ValidateManifest(bad); err == nil {
		t.Fatal("x-chadash-authorize on a plain field")
	}

	// The mark and the interface go together.
	r := NewRegistry()
	if err := r.Register(authFake{fake{authManifest("plex")}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(fake{authManifest("plexish")}); err == nil {
		t.Fatal("a marked field without an Authorizer")
	}
	if err := r.Register(authFake{fake{manifest("tokenless")}}); err == nil {
		t.Fatal("an Authorizer without a marked field")
	}
}
