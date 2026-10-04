package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// runAll is phase 2: with the consented key, run introspection, every read query, the
// out-of-scope checks and one subscription window. Raw responses go to outDir (git-ignored,
// 0700/0600) for sanitizing later; the console only gets status codes, error codes, counts
// and versions.
func runAll(ctx context.Context, c *Client, red *Redactor, outDir string, window time.Duration, w io.Writer) error {
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return err
	}
	save := func(name string, v any) error { return saveRaw(outDir, name, v) }

	summary := map[string]any{"started": time.Now().UTC().Format(time.RFC3339), "scheme": c.base.Scheme}
	tw, row := resultTable(w, red)
	results := map[string]outcome{}
	exec := func(section string, q readQuery) *Result {
		return execRead(ctx, c, red, outDir, section, q, results, row)
	}

	// 1. Introspection. Blocked unless the sandbox is on; then fall back to the root types.
	intro := exec("schema", readQuery{"IntrospectionQuery", "sandbox enabled", introspectionQuery})
	introOK := intro != nil && hasData(intro) && len(intro.Resp.Errors) == 0
	summary["introspection"] = map[string]any{"ok": introOK}
	var roots map[string][]string
	if introOK {
		roots = rootFieldsFromIntrospection(intro.Body)
	} else {
		roots = map[string][]string{}
		for _, q := range rootTypeQueries {
			if res := exec("schema", q); res != nil && hasData(res) {
				name, fields := rootFieldsFromType(res.Resp.Data)
				if name != "" {
					roots[name] = fields
				}
			}
		}
	}
	summary["root_field_counts"] = map[string]int{"Query": len(roots["Query"]), "Mutation": len(roots["Mutation"]), "Subscription": len(roots["Subscription"])}
	_ = save("root-fields", roots)

	// 2. Reads the integration needs.
	var me *Result
	for _, q := range readQueries {
		res := exec("read", q)
		if q.Name == "Me" {
			me = res
		}
		if q.Name == "Info" && res != nil && hasData(res) {
			summary["versions"] = versionsFrom(res.Resp.Data)
		}
	}

	// 3. Reads the key must not be able to do.
	var granted []string
	if me != nil && hasData(me) {
		granted = grantedScopes(me.Resp.Data)
	}
	unexpected, skipped := []string{}, []string{}
	for _, q := range outOfScope {
		if scope, ok := probeOnlyIfRefused[q.Name]; ok && (me == nil || !hasData(me) || slices.Contains(granted, scope)) {
			skipped = append(skipped, q.Name)
			row("scope", q.Name, q.Scope, "-", "-", "-", "-", "skipped: the key may hold it, and the resolver has side effects")
			continue
		}
		if res := exec("scope", q); res != nil && hasData(res) && len(res.Resp.Errors) == 0 {
			unexpected = append(unexpected, q.Name)
		}
	}
	if len(skipped) > 0 {
		summary["out_of_scope_skipped"] = skipped
	}
	summary["out_of_scope_unexpected_success"] = unexpected
	if err := tw.Flush(); err != nil {
		return err
	}

	// 4. What the key says it can do, against what we asked for.
	if me != nil && hasData(me) {
		summary["granted"] = granted
		missing, extra := diffScopes(consentScopes, granted)
		summary["granted_missing"], summary["granted_extra"] = missing, extra
		fmt.Fprintf(w, "\ngranted: %s\n", strings.Join(granted, ","))
		fmt.Fprintf(w, "missing vs request: %s · extra vs request: %s\n", orDash(strings.Join(missing, ",")), orDash(strings.Join(extra, ",")))
		// A key created from the consent URL carries the requested name and description,
		// unless the user edited them on the consent page.
		name, desc := keyLabel(me.Resp.Data)
		asRequested := map[string]bool{"name": name == consentName, "description": desc == consentDescription}
		summary["key_label_as_requested"] = asRequested
		fmt.Fprintf(w, "key name as requested: %v · description as requested: %v\n", asRequested["name"], asRequested["description"])
	}
	if v, ok := summary["versions"]; ok {
		fmt.Fprintf(w, "versions: %v\n", v)
	}
	fmt.Fprintf(w, "root fields: %v · introspection allowed: %v\n", summary["root_field_counts"], introOK)
	if len(unexpected) > 0 {
		fmt.Fprintf(w, "UNEXPECTED: out-of-scope reads succeeded: %s\n", strings.Join(unexpected, ","))
	} else {
		fmt.Fprintln(w, "out-of-scope reads: all refused")
	}

	// 5. One subscription window.
	fmt.Fprintf(w, "\nsubscriptions: opening %d on one socket for up to %s …\n", len(subscriptions), window)
	rep, err := c.Subscribe(ctx, subscriptions, window)
	if rep != nil {
		_ = save("subscriptions", rep)
		stw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintf(stw, "subscription\tnext\terrors\tcodes\tfirst after\tmean gap\tcompleted\n")
		for _, s := range rep.Subs {
			if s.Refused != "" {
				fmt.Fprintf(stw, "%s\trefused by guard: %s\n", s.Name, s.Refused)
				continue
			}
			fmt.Fprintf(stw, "%s\t%d\t%d\t%s\t%s\t%s\t%v\n", s.Name, s.Next, s.Errors, orDash(strings.Join(s.Codes, ",")), orDash(s.First), orDash(s.MeanGap), s.Complete)
		}
		_ = stw.Flush()
		fmt.Fprintf(w, "handshake %d · subprotocol %s · acked %v (%s) · open for %s · close %d %s\n",
			rep.HandshakeStatus, orDash(rep.Subprotocol), rep.Acked, orDash(rep.AckAfter), rep.OpenFor, rep.CloseCode, red.Redact(rep.CloseReason))
		subs := []map[string]any{}
		for _, s := range rep.Subs {
			subs = append(subs, map[string]any{"name": s.Name, "next": s.Next, "errors": s.Errors, "codes": s.Codes, "first_after": s.First, "mean_gap": s.MeanGap, "complete": s.Complete, "refused": s.Refused})
		}
		summary["subscriptions"] = map[string]any{"handshake": rep.HandshakeStatus, "acked": rep.Acked, "open_for": rep.OpenFor, "close_code": rep.CloseCode, "subs": subs}
	}
	if err != nil {
		fmt.Fprintf(w, "subscription error: %s\n", red.Redact(err.Error()))
		summary["subscription_error"] = red.Redact(err.Error())
	}

	summary["queries"] = results
	_ = save("summary", summary)
	fmt.Fprintf(w, "\nraw captures: %d files in the capture directory (git-ignored; real values, do not commit)\n", countFiles(outDir))
	return nil
}

// runOnly re-runs the named reads (from readQueries and outOfScope) and nothing else: no
// introspection, no scope sweep, no subscription window. Raw responses go to outDir as in
// runAll, and the outcomes are appended to the run's summary under "reruns".
func runOnly(ctx context.Context, c *Client, red *Redactor, outDir string, names []string, timeout time.Duration, w io.Writer) error {
	byName := map[string]readQuery{}
	for _, q := range append(append([]readQuery{}, readQueries...), outOfScope...) {
		byName[q.Name] = q
	}
	var qs []readQuery
	for _, n := range names {
		q, ok := byName[strings.TrimSpace(n)]
		if !ok {
			return fmt.Errorf("unknown read %q", n)
		}
		qs = append(qs, q)
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return err
	}
	rerun := map[string]any{"started": time.Now().UTC().Format(time.RFC3339), "timeout_ms": timeout.Milliseconds()}
	tw, row := resultTable(w, red)
	results := map[string]outcome{}
	for _, q := range qs {
		execRead(ctx, c, red, outDir, "rerun", q, results, row)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	rerun["queries"] = results

	summary := map[string]any{}
	if b, err := os.ReadFile(filepath.Join(outDir, "summary.raw.json")); err == nil {
		_ = json.Unmarshal(b, &summary)
	}
	reruns, _ := summary["reruns"].([]any)
	summary["reruns"] = append(reruns, rerun)
	return saveRaw(outDir, "summary", summary)
}

// outcome is one read's result as recorded in the summary: no bodies, no values.
type outcome struct {
	Status int      `json:"status"`
	Codes  []string `json:"codes,omitempty"`
	Data   bool     `json:"data"`
	Bytes  int      `json:"bytes"`
	MS     int64    `json:"ms"`
	Err    string   `json:"error,omitempty"`
}

// execRead runs one read, saves its body under outDir and records and prints its outcome.
func execRead(ctx context.Context, c *Client, red *Redactor, outDir, section string, q readQuery, results map[string]outcome, row func(...any)) *Result {
	start := time.Now()
	res, err := c.Query(ctx, q.Name, q.Doc, nil)
	if err != nil {
		results[q.Name] = outcome{Err: red.Redact(err.Error()), MS: time.Since(start).Milliseconds()}
		row(section, q.Name, q.Scope, "-", "-", "-", "-", red.Redact(err.Error()))
		return nil
	}
	o := outcome{Status: res.Status, Codes: res.codes(), Bytes: len(res.Body), MS: res.Elapsed.Milliseconds()}
	o.Data = hasData(res)
	results[q.Name] = o
	_ = saveResult(outDir, q.Name, res)
	row(section, q.Name, q.Scope, res.Status, orDash(strings.Join(o.Codes, ",")), yesNo(o.Data), o.Bytes, res.Elapsed.Round(time.Millisecond))
	return res
}

// resultTable returns the console table and a row printer that redacts every cell.
func resultTable(w io.Writer, red *Redactor) (*tabwriter.Writer, func(...any)) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	row := func(cols ...any) {
		for i, col := range cols {
			if i > 0 {
				fmt.Fprint(tw, "\t")
			}
			fmt.Fprint(tw, red.Redact(fmt.Sprint(col)))
		}
		fmt.Fprintln(tw)
	}
	row("section", "operation", "needs", "status", "graphql codes", "data", "bytes", "time")
	return tw, row
}

// saveRaw writes v as <name>.raw.json under dir (0600). Raw files hold real values.
func saveRaw(dir, name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name+".raw.json"), b, 0o600)
}

// saveResult writes a response body: parsed JSON as <name>.raw.json, anything else as .raw.txt.
func saveResult(dir, name string, res *Result) error {
	if res.JSON {
		var v any
		if json.Unmarshal(res.Body, &v) == nil {
			return saveRaw(dir, name, v)
		}
	}
	return os.WriteFile(filepath.Join(dir, name+".raw.txt"), res.Body, 0o600)
}

func hasData(r *Result) bool {
	if r == nil || !r.JSON || len(r.Resp.Data) == 0 || string(r.Resp.Data) == "null" {
		return false
	}
	// {"info": null} counts as no data: the field was refused.
	var m map[string]json.RawMessage
	if json.Unmarshal(r.Resp.Data, &m) == nil {
		for _, v := range m {
			if string(v) != "null" {
				return true
			}
		}
		return false
	}
	return true
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func rootFieldsFromIntrospection(body []byte) map[string][]string {
	var r struct {
		Data struct {
			Schema struct {
				QueryType, MutationType, SubscriptionType *struct{ Name string }
				Types                                     []struct {
					Name   string
					Fields []struct{ Name string }
				}
			} `json:"__schema"`
		}
	}
	out := map[string][]string{}
	if json.Unmarshal(body, &r) != nil {
		return out
	}
	s := r.Data.Schema
	want := map[string]string{}
	if s.QueryType != nil {
		want[s.QueryType.Name] = "Query"
	}
	if s.MutationType != nil {
		want[s.MutationType.Name] = "Mutation"
	}
	if s.SubscriptionType != nil {
		want[s.SubscriptionType.Name] = "Subscription"
	}
	for _, t := range s.Types {
		if root, ok := want[t.Name]; ok {
			for _, f := range t.Fields {
				out[root] = append(out[root], f.Name)
			}
			sort.Strings(out[root])
		}
	}
	return out
}

func rootFieldsFromType(data json.RawMessage) (string, []string) {
	var r struct {
		Type *struct {
			Name   string
			Fields []struct{ Name string }
		} `json:"__type"`
	}
	if json.Unmarshal(data, &r) != nil || r.Type == nil {
		return "", nil
	}
	var names []string
	for _, f := range r.Type.Fields {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	return r.Type.Name, names
}

func versionsFrom(data json.RawMessage) map[string]string {
	var r struct {
		Info struct {
			Versions struct {
				Core map[string]string `json:"core"`
			} `json:"versions"`
		} `json:"info"`
	}
	_ = json.Unmarshal(data, &r)
	return r.Info.Versions.Core
}

// grantedScopes turns me.permissions into "resource:action" strings, lower-case like the
// consent scopes, plus "role:x" for each role.
func grantedScopes(data json.RawMessage) []string {
	var r struct {
		Me struct {
			Roles       []string `json:"roles"`
			Permissions []struct {
				Resource string   `json:"resource"`
				Actions  []string `json:"actions"`
			} `json:"permissions"`
		} `json:"me"`
	}
	_ = json.Unmarshal(data, &r)
	var out []string
	for _, role := range r.Me.Roles {
		out = append(out, "role:"+strings.ToLower(role))
	}
	for _, p := range r.Me.Permissions {
		for _, a := range p.Actions {
			out = append(out, strings.ToLower(p.Resource)+":"+strings.ToLower(a))
		}
	}
	sort.Strings(out)
	return out
}

// keyLabel returns the key's name and description from me.
func keyLabel(data json.RawMessage) (name, description string) {
	var r struct {
		Me struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"me"`
	}
	_ = json.Unmarshal(data, &r)
	return r.Me.Name, r.Me.Description
}

func diffScopes(want, got []string) (missing, extra []string) {
	g := map[string]bool{}
	for _, s := range got {
		g[s] = true
	}
	w := map[string]bool{}
	for _, s := range want {
		w[s] = true
		if !g[s] {
			missing = append(missing, s)
		}
	}
	for _, s := range got {
		if !w[s] {
			extra = append(extra, s)
		}
	}
	return missing, extra
}

func countFiles(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	return len(entries)
}
