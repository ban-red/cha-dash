package authz

// Agent tokens (ADR-0017; docs/plan/14-v0.4.md, "Agents"). An agent reads
// and proposes; it never acts. What that means here:
//
//   - Routes: an agent token calls POST /mcp (Access AgentToken) and
//     nothing else. Guard refuses it everywhere else with agent_mcp_only,
//     and refuses a plain API token or a session on /mcp.
//   - Scopes: its own vocabulary. Reads are graph.read, inbox.read and
//     logs.read (off unless asked for), the first two narrowable to a
//     host, a tag or a lens: graph.read:host=jupiter. Proposals are
//     propose:<action or family.*>, qualifiable like RBAC scopes:
//     propose:docker.container.restart:host=jupiter. Never "*".
//   - The policy (Decide): an agent acts for the user who minted it, never
//     above their current role, narrowed to its propose scopes; it can't
//     propose T4, settings (owned stacks) or the hub itself, nor an action
//     that reads data back without logs.read; and Agents must be on. It
//     needs no step-up to propose, because it never runs anything: every
//     plan it makes waits for a person (NeedsApproval), who approves it in
//     a session with a step-up from T2 (DecideApproval).

import (
	"context"
	"slices"
	"strings"
	"time"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/msg"
)

// Agent token scopes beside graph.read and inbox.read.
const (
	// ScopeLogsRead lets an agent propose an action that reads data back
	// (a container's logs) and see what it read. Off unless the token asks
	// for it: logs are the easiest place to plant text.
	ScopeLogsRead = "logs.read"
	// ProposePrefix starts an agent's action scopes: propose:<action>.
	ProposePrefix = "propose:"
)

// AgentReadScopes are the reads an agent token may hold, in the order the
// page shows them. MinRole is as for ReadScopes.
var AgentReadScopes = []ReadScope{
	{ScopeGraphRead, msg.T("agents.read_graph", nil), msg.T("agents.read_graph_about", nil), auth.RoleHousehold},
	{ScopeInboxRead, msg.T("agents.read_inbox", nil), msg.T("agents.read_inbox_about", nil), auth.RoleHousehold},
	{ScopeLogsRead, msg.T("agents.read_logs", nil), msg.T("agents.read_logs_about", nil), auth.RoleOperator},
}

// agentNarrowing are the qualifiers a read scope may carry.
var agentNarrowing = []string{"host", "tag", "lens"}

// TargetFacts is what a resource's narrowing qualifiers match: the hosts it
// runs on (IDs and names, itself included) and its tags. The hub reads them
// from the Home Graph (mcp.Facts).
type TargetFacts struct {
	Hosts []string
	Tags  []string
}

// AgentForbiddenAction reports whether agents may never propose action:
// owned stacks (settings) and the hub itself (chadash.*).
func AgentForbiddenAction(action string) bool {
	return settingsAction(action) || strings.HasPrefix(action, "chadash.")
}

// ValidAgentScope checks a scope an agent token of a user with role may
// hold. It returns a sentence for the person when it can't. tier, when
// known, is the action's tier (for propose scopes naming one action).
func ValidAgentScope(scope, role string, tierOf func(action string) (Tier, bool)) error {
	if scope == "*" {
		return refuse(ErrForbidden, msg.New("agents.scope_everything"))
	}
	if rest, ok := strings.CutPrefix(scope, ProposePrefix); ok {
		sc, err := ParseScope(rest)
		if err != nil || sc.Action == "*" {
			return refuse(ErrForbidden, msg.New("agents.scope_invalid", "scope", scope))
		}
		// A family pattern ("stack.*") is checked by its prefix.
		if AgentForbiddenAction(strings.TrimSuffix(sc.Action, "*")) {
			return refuse(ErrForbidden, msg.New("agents.scope_config", "action", sc.Action))
		}
		if tierOf != nil {
			if t, ok := tierOf(sc.Action); ok && t.rank() >= T4.rank() {
				return refuse(ErrForbidden, msg.New("agents.scope_t4", "action", sc.Action))
			}
		}
		return nil
	}
	sc, err := ParseScope(scope)
	if err != nil {
		return refuse(ErrForbidden, msg.New("agents.scope_invalid", "scope", scope))
	}
	i := slices.IndexFunc(AgentReadScopes, func(r ReadScope) bool { return r.Scope == sc.Action })
	if i < 0 {
		return refuse(ErrForbidden, msg.New("agents.scope_invalid", "scope", scope))
	}
	for k := range sc.Quals {
		if !slices.Contains(agentNarrowing, k) || sc.Action == ScopeLogsRead {
			return refuse(ErrForbidden, msg.New("agents.scope_qualifier", "scope", scope))
		}
	}
	if rank(role) < rank(AgentReadScopes[i].MinRole) {
		return refuse(ErrForbidden, msg.New("agents.scope_role", "role", role, "scope", sc.Action))
	}
	return nil
}

// cleanAgentScopes is cleanScopes for an agent token.
func cleanAgentScopes(in []string, role string, tierOf func(string) (Tier, bool)) ([]string, error) {
	if len(in) == 0 {
		return nil, refuse(ErrForbidden, msg.New("agents.scope_none"))
	}
	if len(in) > MaxTokenScopes {
		return nil, forbid("A token can have at most %d scopes. Use a pattern such as proxmox.guest.* instead.", MaxTokenScopes)
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if err := ValidAgentScope(s, role, tierOf); err != nil {
			return nil, err
		}
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out, nil
}

// ProposeGrants are an agent token's propose scopes as action grants
// (the prefix dropped), never nil: an agent with none may propose nothing.
func ProposeGrants(scopes []string) []string {
	out := []string{}
	for _, s := range scopes {
		if rest, ok := strings.CutPrefix(s, ProposePrefix); ok {
			out = append(out, rest)
		}
	}
	return out
}

// AgentReads are an agent token's read scopes of one kind (graph.read,
// inbox.read), parsed: nil when it has none, an empty-qualifier Scope when
// it may read everything its role sees.
func AgentReads(scopes []string, read string) []Scope {
	var out []Scope
	for _, s := range scopes {
		if strings.HasPrefix(s, ProposePrefix) {
			continue
		}
		if sc, err := ParseScope(s); err == nil && sc.Action == read {
			out = append(out, sc)
		}
	}
	return out
}

// HasScope reports whether scopes hold exactly scope.
func HasScope(scopes []string, scope string) bool { return slices.Contains(scopes, scope) }

// AgentActorName names an agent token's actor in the audit log and the
// Inbox, as TokenActorName does a token's.
func AgentActorName(token, user string) string { return TokenActorName(token, user) }

// agent resolves an agent actor: Agents on, the token still valid and an
// agent token, its user's current role and its scopes.
func (p *Policy) agent(ctx context.Context, a Actor) (Actor, []string, error) {
	if p.agentsOn == nil {
		return a, nil, refuse(ErrForbidden, msg.New("agents.off"))
	}
	on, err := p.agentsOn(ctx)
	if err != nil {
		p.log.Error("authz: reading the Agents setting failed; refusing", "err", err)
		return a, nil, refuse(ErrForbidden, msg.New("agents.check_failed"))
	}
	if !on {
		return a, nil, refuse(ErrForbidden, msg.New("agents.off"))
	}
	if p.tokens == nil {
		return a, nil, refuse(ErrForbidden, msg.New("agents.check_failed"))
	}
	acc, err := p.tokens(ctx, a.ID)
	if err != nil {
		p.log.Error("authz: reading an agent token failed; refusing", "err", err, "token", a.ID)
		return a, nil, refuse(ErrForbidden, msg.New("agents.check_failed"))
	}
	if !acc.OK {
		return a, nil, refuse(ErrForbidden, msg.New("agents.token_gone"))
	}
	if acc.Kind != auth.KindAgent {
		return a, nil, refuse(ErrForbidden, msg.New("agents.not_an_agent"))
	}
	a.Role, a.StepUpUntil = acc.Role, time.Time{}
	return a, acc.Scopes, nil
}

// WithAgentsSwitch makes agent actors depend on the Agents switch
// (security.Service.AgentsOn): while it's off, or can't be read, every
// agent is refused, so a proposal left waiting can't be approved either.
// Without it every agent is refused.
func WithAgentsSwitch(f func(context.Context) (bool, error)) PolicyOption {
	return func(p *Policy) { p.agentsOn = f }
}

// WithTargetFacts lets host= and tag= qualifiers match a target: f returns
// the hosts it runs on and its tags. Without it they match nothing.
func WithTargetFacts(f func(ctx context.Context, target string) TargetFacts) PolicyOption {
	return func(p *Policy) { p.facts = f }
}

// refuse is a Refusal with a catalog sentence.
func refuse(err error, m msg.M) *Refusal { return &Refusal{Err: err, Msg: m.Text()} }
