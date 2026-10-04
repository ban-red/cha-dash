package main

import (
	"errors"
	"fmt"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

// ErrMutation is returned for any document that contains a mutation. The spike is read-only:
// the check runs before a single byte goes on the wire, for HTTP and WebSocket alike.
var ErrMutation = errors.New("refused: the document contains a mutation, and this spike is read-only")

// ErrNotAllowed is returned for documents that aren't a mutation but still aren't something
// the transport may send (a subscription over HTTP, a query over the subscription socket, an
// unparseable document, a fragments-only document).
var ErrNotAllowed = errors.New("refused: operation not allowed")

// checkReadOnly parses doc and fails closed. Every operation in the document must be one of
// allowed (a document may hold several operations; one mutation anywhere refuses all of it,
// whatever operationName selects). A document that doesn't parse is refused too, so a
// mutation can't slip through a parse we didn't understand.
func checkReadOnly(doc string, allowed ...ast.Operation) error {
	q, perr := parser.ParseQuery(&ast.Source{Name: "spike", Input: doc})
	if perr != nil {
		return fmt.Errorf("%w: document does not parse: %v", ErrNotAllowed, perr)
	}
	if len(q.Operations) == 0 {
		return fmt.Errorf("%w: document has no operation", ErrNotAllowed)
	}
	for _, op := range q.Operations {
		if op.Operation == ast.Mutation {
			return ErrMutation
		}
	}
	for _, op := range q.Operations {
		ok := false
		for _, a := range allowed {
			if op.Operation == a {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("%w: %s operations are not allowed on this transport", ErrNotAllowed, op.Operation)
		}
	}
	return nil
}
