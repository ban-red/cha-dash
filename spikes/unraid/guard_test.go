package main

import (
	"errors"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
)

func TestGuardAllowsReads(t *testing.T) {
	for name, doc := range map[string]string{
		"named query":          `query Info { info { os { distro } } }`,
		"shorthand query":      `{ info { os { distro } } }`,
		"with fragment":        `query A { array { disks { ...D } } } fragment D on ArrayDisk { name }`,
		"mutation in a string": `query N { notifications { list(filter: {type: UNREAD, offset: 0, limit: 1, q: "mutation { x }"}) { id } } }`,
		"mutation in comment":  "# mutation { deleteNotification }\nquery Q { online }",
		"field named mutation": `query Q { mutation { id } }`,
		"every read query":     readQueries[0].Doc,
	} {
		if err := checkReadOnly(doc, ast.Query); err != nil {
			t.Errorf("%s: refused a read: %v", name, err)
		}
	}
	for _, q := range append(append(append([]readQuery{}, readQueries...), outOfScope...), rootTypeQueries...) {
		if err := checkReadOnly(q.Doc, ast.Query); err != nil {
			t.Errorf("%s: refused one of our own reads: %v", q.Name, err)
		}
	}
	if err := checkReadOnly(introspectionQuery, ast.Query); err != nil {
		t.Errorf("introspection refused: %v", err)
	}
	for _, s := range subscriptions {
		if err := checkReadOnly(s.Doc, ast.Subscription); err != nil {
			t.Errorf("%s: refused one of our own subscriptions: %v", s.Name, err)
		}
	}
}

func TestGuardRefusesMutations(t *testing.T) {
	for name, doc := range map[string]string{
		"plain":                `mutation { archiveAll { unread { total } } }`,
		"named":                `mutation Stop { docker { stop(id: "x") { id } } }`,
		"after a comment":      "# query Info { info { time } }\nmutation M { vm { start(id: \"x\") } }",
		"second operation":     `query Ok { online } mutation Bad { archiveAll { unread { total } } }`,
		"first operation":      `mutation Bad { archiveAll { unread { total } } } query Ok { online }`,
		"with variables":       `mutation ($id: PrefixedID!) { array { setState(input: {desiredState: STOP}) { state } } }`,
		"leading whitespace":   "\n\t  mutation{archiveAll{unread{total}}}",
		"with fragment spread": `mutation M { apiKey { ...K } } fragment K on ApiKeyMutations { __typename }`,
	} {
		for _, allowed := range [][]ast.Operation{{ast.Query}, {ast.Subscription}, {ast.Query, ast.Subscription, ast.Mutation}} {
			err := checkReadOnly(doc, allowed...)
			if !errors.Is(err, ErrMutation) {
				t.Errorf("%s (allowed %v): want ErrMutation, got %v", name, allowed, err)
			}
		}
	}
}

func TestGuardFailsClosed(t *testing.T) {
	for name, doc := range map[string]string{
		"empty":           ``,
		"whitespace":      "   \n",
		"garbage":         `this is not graphql`,
		"unbalanced":      `query { info { os }`,
		"upper-case kw":   `MUTATION { archiveAll { unread { total } } }`,
		"fragments only":  `fragment D on ArrayDisk { name }`,
		"type definition": `type Query { x: Int }`,
	} {
		err := checkReadOnly(doc, ast.Query)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if !errors.Is(err, ErrNotAllowed) && !errors.Is(err, ErrMutation) {
			t.Errorf("%s: unexpected error type %v", name, err)
		}
	}
}

func TestGuardKeepsTransportsApart(t *testing.T) {
	if err := checkReadOnly(`subscription S { systemMetricsCpu { percentTotal } }`, ast.Query); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("subscription over HTTP: want ErrNotAllowed, got %v", err)
	}
	if err := checkReadOnly(`query Q { online }`, ast.Subscription); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("query over the subscription socket: want ErrNotAllowed, got %v", err)
	}
}
