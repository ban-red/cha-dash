package topics

import (
	"testing"
	"time"

	"dash.cha.sh/internal/graph"
)

// Replace is the whole set: a symptom left out loses its causes, and the
// topic never carries a path.
func TestCauseSetReplace(t *testing.T) {
	s := New(graph.New(), time.Now())
	path := []Step{{From: "a", Type: "depends_on", To: "b"}}
	if err := s.Causes.Replace(map[string][]Cause{
		"a": {{Text: "b is down.", Resource: "b", Rule: "depends_on", Path: path}},
		"c": {{Text: "d is down.", Resource: "d", Rule: "runs_on"}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := s.Causes.Of("a"); len(got) != 1 || len(got[0].Path) != 1 {
		t.Fatalf("Of(a) = %+v", got)
	}
	if b := s.Causes.Doc.Item(Causes, "a"); string(b) != `[{"text":"b is down.","resource":"b","rule":"depends_on"}]` {
		t.Fatalf("topic item a = %s", b)
	}
	if err := s.Causes.Replace(map[string][]Cause{"a": {{Text: "b is down.", Resource: "b", Rule: "depends_on"}}, "e": nil}); err != nil {
		t.Fatal(err)
	}
	if s.Causes.Doc.Item(Causes, "c") != nil || s.Causes.Of("c") != nil || s.Causes.Doc.Item(Causes, "e") != nil {
		t.Fatal("c's causes stayed, or e got an empty entry")
	}
	if got := s.Causes.Of("a"); len(got) != 1 || got[0].Path != nil {
		t.Fatalf("Of(a) after replace = %+v", got)
	}
}
