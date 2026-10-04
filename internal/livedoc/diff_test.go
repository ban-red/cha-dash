package livedoc

import (
	"bytes"
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"
)

// splitMatches checks members against encoding/json on one encoding.
func splitMatches(t *testing.T, b []byte) {
	t.Helper()
	var want map[string]json.RawMessage
	wantOK := json.Unmarshal(b, &want) == nil && bytes.HasPrefix(bytes.TrimSpace(b), []byte("{"))
	got, ok := members(b, nil)
	if ok != wantOK {
		t.Fatalf("members(%s) ok = %v, want %v", b, ok, wantOK)
	}
	if !ok {
		return
	}
	if len(got) != len(want) {
		t.Fatalf("members(%s) = %d members, want %d", b, len(got), len(want))
	}
	for _, m := range got {
		w, found := want[string(m.key)]
		if !found || !bytes.Equal(m.val, w) {
			t.Fatalf("members(%s): %q = %s, want %s", b, m.key, m.val, w)
		}
	}
}

func TestMembers(t *testing.T) {
	for _, s := range []string{
		`{}`,
		` { } `,
		`{"a":1}`,
		`{"a":1,"b":"two","c":null,"d":true,"e":false,"f":-1.5e-3}`,
		`{"s":"a \"quoted\" }{ ][ , : string\\","t":"\\"}`,
		`{"nested":{"a":[1,{"b":"]}"},[]],"c":{}},"after":[[],[[]]]}`,
		`{"k\"ey":1,"é":2,"a/b":3,"c~d":4,"<b>":5}`,
		"{ \"a\" : 1 ,\n\t\"b\" : [ 1 , 2 ] }",
		// Not objects.
		`null`, `1`, `"{}"`, `[{"a":1}]`, ``,
	} {
		splitMatches(t, []byte(s))
	}
	// What a scan can't follow is refused, never misread.
	for _, s := range []string{`{"a":1`, `{"a"}`, `{"a":}`, `{"a":1}x`, `{"a":"x}`, `{"a":[1}`, `{1:2}`} {
		if _, ok := members([]byte(s), nil); ok {
			t.Errorf("members(%s) accepted it", s)
		}
	}
}

func TestMemberOpsFallsBackToWhole(t *testing.T) {
	var d differ
	for name, tc := range map[string]struct{ old, new string }{
		"duplicate key in new": {`{"a":1,"b":2,"pad":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`, `{"a":1,"a":3,"b":2,"pad":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`},
		"duplicate key in old": {`{"a":1,"a":2,"b":1,"pad":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`, `{"a":2,"b":2,"pad":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`},
		"not an object":        {`[1,2,3]`, `[1,2,4]`},
		"unscannable":          {`{"a":1,"pad":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`, `{"a":2,"pad":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"`},
	} {
		ops := d.memberOps(nil, "/m", json.RawMessage(tc.old), json.RawMessage(tc.new))
		if len(ops) != 1 || ops[0].Path != "/m" || ops[0].Op != "replace" {
			t.Errorf("%s: %v, want the member replaced whole", name, opStrings(t, ops))
		}
	}
	// The same scratch space still diffs normally afterwards.
	ops := d.memberOps(nil, "/m", json.RawMessage(`{"a":1,"pad":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`), json.RawMessage(`{"a":2,"pad":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`))
	if got := opStrings(t, ops); len(got) != 1 || got[0] != "replace /m/a 2" {
		t.Fatalf("after fallbacks: %q", got)
	}
}

// TestMembersRandom compares members with encoding/json on random objects
// written by json.Marshal, the only encodings Doc stores.
func TestMembersRandom(t *testing.T) {
	rng := rand.New(rand.NewPCG(9, 10))
	pieces := []string{"a", "/", "~", "\"", "\\", "}", "{", "]", "[", ",", ":", " ", "é", "<", " ", "\x01"}
	str := func() string {
		var b strings.Builder
		for range rng.IntN(5) {
			b.WriteString(pieces[rng.IntN(len(pieces))])
		}
		return b.String()
	}
	var value func(depth int) any
	value = func(depth int) any {
		switch n := rng.IntN(8); {
		case n == 0:
			return nil
		case n == 1:
			return rng.IntN(2) == 0
		case n == 2:
			return rng.NormFloat64() * 1e6
		case n == 3:
			return str()
		case n == 4 && depth < 3:
			a := make([]any, rng.IntN(4))
			for i := range a {
				a[i] = value(depth + 1)
			}
			return a
		case n >= 5 && depth < 3:
			m := map[string]any{}
			for range rng.IntN(5) {
				m[str()] = value(depth + 1)
			}
			return m
		default:
			return rng.IntN(1000)
		}
	}
	for range 3000 {
		b, err := json.Marshal(value(0))
		if err != nil {
			t.Fatal(err)
		}
		splitMatches(t, b)
	}
}
