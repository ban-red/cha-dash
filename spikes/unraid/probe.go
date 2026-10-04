package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

// runProbe answers "is the API there, and what does it allow without a key?" using only
// unauthenticated, read-only requests. It prints HTTP status codes, GraphQL error codes and
// where redirects point (path only); never response bodies.
func runProbe(ctx context.Context, c *Client, red *Redactor, w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "#\tcheck\trequest\tstatus\tgraphql codes\tdata\tnote\ttime\n")

	type step struct {
		name, method, path, accept string
		op, doc                    string
	}
	steps := []step{
		{name: "webGUI root", method: "GET", path: "/", accept: "text/html"},
		{name: "consent page route", method: "GET", path: consentPath, accept: "text/html"},
		{name: "sandbox page", method: "GET", path: "/graphql", accept: "text/html"},
		{name: "API answers", method: "POST", path: "/graphql", op: "Typename", doc: `query Typename { __typename }`},
		{name: "public field", method: "POST", path: "/graphql", op: "PublicSso", doc: `query PublicSso { isSSOEnabled }`},
		{name: "introspection", method: "POST", path: "/graphql", op: "IntrospectionQuery", doc: `query IntrospectionQuery { __schema { queryType { name } } }`},
		{name: "root __type", method: "POST", path: "/graphql", op: "RootQueryType", doc: `query RootQueryType { __type(name: "Query") { name } }`},
		{name: "protected read", method: "POST", path: "/graphql", op: "Version", doc: `query Version { info { versions { core { unraid api } } } }`},
	}

	for i, s := range steps {
		var res *Result
		var err error
		if s.method == "GET" {
			res, err = c.Get(ctx, s.path, s.accept)
		} else {
			res, err = c.Query(ctx, s.op, s.doc, nil)
		}
		req := s.method + " " + s.path
		if s.op != "" {
			req += " " + s.op
		}
		if err != nil {
			fmt.Fprintf(tw, "%d\t%s\t%s\t-\t-\t-\t%s\t-\n", i+1, s.name, req, red.Redact(err.Error()))
			continue
		}
		codes, data := "-", "-"
		if res.JSON {
			if cs := res.codes(); len(cs) > 0 {
				codes = strings.Join(cs, ",")
			}
			data = "null"
			if len(res.Resp.Data) > 0 && string(res.Resp.Data) != "null" {
				data = "present"
			}
		}
		note := red.Redact(c.describeLocation(res.Location))
		if note == "" && !res.JSON && s.method == "POST" {
			note = "not a GraphQL response"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%s\t%s\t%s\t%s\n", i+1, s.name, req, res.Status, codes, data, note, res.Elapsed.Round(time.Millisecond))
	}

	// Subscription transport: handshake and connection_init without a key, no subscribe.
	rep, err := c.Subscribe(ctx, nil, 15*time.Second)
	note := ""
	if err != nil {
		note = red.Redact(err.Error())
	}
	if rep.CloseCode != 0 {
		note = strings.TrimSpace(fmt.Sprintf("%s close %d %s", note, rep.CloseCode, red.Redact(rep.CloseReason)))
	}
	ack := "no ack"
	if rep.Acked {
		ack = "ack after " + rep.AckAfter
	}
	fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t-\t-\t%s\t-\n", len(steps)+1, "graphql-ws handshake",
		"WS /graphql ("+wsSubprotocol+")", rep.HandshakeStatus, strings.TrimSpace("subprotocol="+orDash(rep.Subprotocol)+", "+ack+" "+note))

	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(w, "\nscheme: %s · key sent: no · redirects followed: no\n", c.base.Scheme)
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// writeConsentURL builds the consent URL and writes it, with a fresh state, to path (0600).
// Only the parts that don't identify the server are printed.
func writeConsentURL(cfg instanceConfig, path string, w io.Writer) error {
	state, err := newState()
	if err != nil {
		return err
	}
	u, err := buildConsentURL(cfg.URL, consentName, consentDescription, consentScopes, consentRedirect, state)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(u+"\n"), 0o600); err != nil {
		return fmt.Errorf("write consent URL: %w", err)
	}
	fmt.Fprintf(w, "consent URL written (%d chars, mode 0600)\n", len(u))
	fmt.Fprintf(w, "path:         %s\n", consentPath)
	fmt.Fprintf(w, "name:         %s\n", consentName)
	fmt.Fprintf(w, "description:  %s\n", consentDescription)
	fmt.Fprintf(w, "scopes:       %s\n", strings.Join(consentScopes, ","))
	fmt.Fprintf(w, "redirect_uri: %s\n", consentRedirect)
	fmt.Fprintf(w, "state:        %s\n", state)
	return nil
}
