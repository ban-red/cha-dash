// Package msg is the catalog of sentences the hub says to people. Each
// sentence has a stable ID, "<area>.<what>" such as "auth.signin_required",
// and English text with named parameters in braces: "{name} isn't running".
// An API error carries the ID and its arguments beside the English text, so
// a translation can be picked by ID later.
//
// The catalog lives in this package, one file per area, so msg imports
// nothing from the hub and every package can use it. A brace that doesn't
// hold a name ({"password"}) is plain text.
//
// Mistakes in a call (an unknown ID, a missing or extra argument) are found
// by the package's tests, never by a panic in a request: at run time a
// sentence that can't be rendered reads as its ID and arguments.
package msg

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// M is one sentence to say: its ID and the values of its parameters.
type M struct {
	ID   string         `json:"id"`
	Args map[string]any `json:"args,omitempty"`
}

// New returns the sentence id with its arguments as name, value pairs:
// msg.New("node.down", "name", n.Name).
func New(id string, kv ...any) M {
	m := M{ID: id}
	if len(kv) > 0 {
		m.Args = make(map[string]any, len(kv)/2)
		for i := 0; i+1 < len(kv); i += 2 {
			k, ok := kv[i].(string)
			if !ok {
				k = fmt.Sprint(kv[i])
			}
			m.Args[k] = kv[i+1]
		}
		if len(kv)%2 == 1 {
			m.Args[fmt.Sprint(kv[len(kv)-1])] = nil
		}
	}
	return m
}

// Text renders m in English.
func (m M) Text() string { return T(m.ID, m.Args) }

// T renders the sentence id in English with args. An unknown ID, or args
// that don't match the sentence's parameters, render as the ID and args.
func T(id string, args map[string]any) string {
	text, ok := catalog[id]
	if !ok || !argsMatch(text, args) {
		return fallback(id, args)
	}
	if len(args) == 0 {
		return text
	}
	return param.ReplaceAllStringFunc(text, func(p string) string {
		return fmt.Sprint(args[p[1:len(p)-1]])
	})
}

// Lookup returns the English text of id, parameters unfilled.
func Lookup(id string) (string, bool) {
	text, ok := catalog[id]
	return text, ok
}

// Params returns the parameter names in text, sorted, each once.
func Params(text string) []string {
	var names []string
	for _, p := range param.FindAllString(text, -1) {
		names = append(names, p[1:len(p)-1])
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// IDs returns every registered ID, sorted.
func IDs() []string {
	ids := make([]string, 0, len(catalog))
	for id := range catalog {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// Check returns the catalog's problems: malformed IDs, IDs registered
// twice, empty text. Tests call it; nothing else should need to.
func Check() []string {
	problems := slices.Clone(registerProblems)
	for _, id := range IDs() {
		if !validID.MatchString(id) {
			problems = append(problems, fmt.Sprintf("%s: an ID is <area>.<what>, lowercase, with dots and underscores", id))
		}
		if strings.TrimSpace(catalog[id]) == "" {
			problems = append(problems, fmt.Sprintf("%s: no text", id))
		}
	}
	return problems
}

// CheckArgs returns the problems with saying id with args named keys: an
// unknown ID, a parameter with no argument, an argument with no parameter.
func CheckArgs(id string, keys []string) []string {
	text, ok := catalog[id]
	if !ok {
		return []string{fmt.Sprintf("%s: no such message", id)}
	}
	want := Params(text)
	have := slices.Sorted(slices.Values(keys))
	var problems []string
	for _, p := range want {
		if !slices.Contains(have, p) {
			problems = append(problems, fmt.Sprintf("%s: missing argument {%s}", id, p))
		}
	}
	for _, k := range have {
		if !slices.Contains(want, k) {
			problems = append(problems, fmt.Sprintf("%s: extra argument %q", id, k))
		}
	}
	return problems
}

var (
	param   = regexp.MustCompile(`\{[a-z][a-z0-9_]*\}`)
	validID = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$`)

	catalog          = map[string]string{}
	registerProblems []string
)

// register adds an area's sentences. Each area's file calls it from init.
func register(entries map[string]string) {
	for id, text := range entries {
		if _, dup := catalog[id]; dup {
			registerProblems = append(registerProblems, fmt.Sprintf("%s: registered twice", id))
			continue
		}
		catalog[id] = text
	}
}

func argsMatch(text string, args map[string]any) bool {
	want := Params(text)
	if len(want) != len(args) {
		return false
	}
	for _, p := range want {
		if _, ok := args[p]; !ok {
			return false
		}
	}
	return true
}

// fallback is "id" or "id (a=1, b=2)", arguments sorted by name.
func fallback(id string, args map[string]any) string {
	if len(args) == 0 {
		return id
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%v", k, args[k])
	}
	return id + " (" + strings.Join(parts, ", ") + ")"
}
