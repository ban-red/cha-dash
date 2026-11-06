package apidoc

import (
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/nodes"
	"dash.cha.sh/internal/stacks"
)

// Mirrors of what internal/nodes' and internal/stacks' handlers read and
// write.
type (
	nodeList struct {
		Nodes   []nodes.Status `json:"nodes"`
		HubPin  string         `json:"hub_pin"`
		Pairing bool           `json:"pairing"`
	}
	joinTokenList struct {
		Tokens []nodes.JoinToken `json:"tokens"`
	}
	newJoinToken struct {
		Name       string `json:"name"`
		TTLMinutes int    `json:"ttl_minutes"`
		AddDocker  *bool  `json:"add_docker"`
	}
	joinTokenCreated struct {
		Token   nodes.JoinToken `json:"token"`
		Secret  string          `json:"secret"`
		HubURL  string          `json:"hub_url"`
		Env     string          `json:"env"`
		Command string          `json:"command"`
	}

	stackList struct {
		Nodes []stacks.NodeView `json:"nodes"`
	}
	secretItem struct {
		Name string `json:"name"`
		Set  bool   `json:"set"`
	}
	secretList struct {
		Secrets []secretItem `json:"secrets"`
	}
	secretValue struct {
		Value *string `json:"value"`
	}
	draftRequest struct {
		Content  string `json:"content"`
		Revision int64  `json:"revision"`
	}
	draftBody struct {
		Draft *stacks.Draft `json:"draft"`
	}
	draftConflict struct {
		httpx.ErrorBody
		Revision int64 `json:"revision"`
	}
	contentBody struct {
		Content string `json:"content"`
	}
	syncAuth struct {
		Kind     string `json:"kind"`
		Username string `json:"username"`
		Secret   string `json:"secret"`
	}
	syncRequest struct {
		URL          string   `json:"url"`
		Branch       string   `json:"branch"`
		Path         string   `json:"path"`
		Auth         syncAuth `json:"auth"`
		HostKey      string   `json:"host_key"`
		Mode         string   `json:"mode"`
		PollSeconds  int      `json:"poll_seconds"`
		SecretsAllow []string `json:"secrets_allow"`
	}
	syncBody struct {
		Sync *stacks.SyncView `json:"sync"`
	}
	syncCheck struct {
		URL    string    `json:"url"`
		Branch string    `json:"branch"`
		Auth   *syncAuth `json:"auth"`
	}
	syncTip struct {
		SHA     string  `json:"sha"`
		HostKey *string `json:"host_key"`
	}
	queuedBody struct {
		Queued bool `json:"queued"`
	}
	hookList struct {
		Hooks []stacks.HookView `json:"hooks"`
	}
	newHook struct {
		URL          string `json:"url"`
		Branch       string `json:"branch"`
		Sender       string `json:"sender"`
		DeployOnPush bool   `json:"deploy_on_push"`
	}
	hookCreated struct {
		Hook        stacks.HookView   `json:"hook"`
		Secret      string            `json:"secret"`
		Switched    int               `json:"switched"`
		NotSwitched []stacks.HookSync `json:"not_switched"`
	}
	hookRotated struct {
		Hook   stacks.HookView `json:"hook"`
		Secret string          `json:"secret"`
	}
	hookStatus struct {
		Status string `json:"status"`
	}
	moveRequest struct {
		FromPath string `json:"from_path"`
		Content  string `json:"content"`
	}
)

func init() {
	add("nodes", map[string]Op{
		"GET /api/nodes":                     {Summary: "The nodes, the hub's key pin and whether local pairing is on.", Resp: nodeList{}},
		"POST /api/nodes/{id}/revoke":        {Summary: "Revoke a node, or reject a pending one.", Resp: nodes.Record{}, Errors: []string{"already_revoked"}},
		"POST /api/nodes/{id}/approve":       {Summary: "Approve a node waiting after joining with a token. Needs a step-up.", Resp: nodes.Record{}, Errors: []string{"not_pending", "step_up_required"}},
		"POST /api/nodes/pairing/regenerate": {Summary: "Make a fresh local pairing secret (it stays in the pairing file).", Errors: []string{"pairing_off"}},
		"GET /api/nodes/tokens":              {Summary: "The join tokens.", Resp: joinTokenList{}},
		"POST /api/nodes/tokens":             {Summary: "Make a join token for a node on another host. The secret is in this answer only.", Body: newJoinToken{}, Status: 201, Resp: joinTokenCreated{}},
		"DELETE /api/nodes/tokens/{id}":      {Summary: "Revoke a join token.", Resp: nodes.JoinToken{}, Errors: []string{"token_spent"}},
		"GET /api/nodes/{id}/self-update":    {Summary: "Whether a node can update itself, and how its last update went.", Resp: stacks.SelfUpdateStatus{}},
	})
	add("stacks", map[string]Op{
		"GET /api/stacks":                             {Summary: "Every node's stacks and their status.", Resp: stackList{}},
		"GET /api/stacks/secrets":                     {Summary: "The names of the stack secrets (never their values).", Resp: secretList{}},
		"PUT /api/stacks/secrets/{name}":              {Summary: "Set a stack secret.", Body: secretValue{}},
		"DELETE /api/stacks/secrets/{name}":           {Summary: "Remove a stack secret."},
		"GET /api/stacks/{node}/{stack}":              {Summary: "A stack: its compose file, draft, history and services.", Resp: &stacks.StackDetail{}},
		"GET /api/stacks/{node}/{stack}/logs":         {Summary: "Every container of the stack's log, interleaved and followed live, as server-sent events.", Media: "text/event-stream"},
		"PUT /api/stacks/{node}/{stack}":              {Summary: "Save a draft of a stack's compose file.", Body: draftRequest{}, Resp: draftBody{}, Fail: map[int]any{409: draftConflict{}}, Errors: []string{"protected", "synced"}},
		"DELETE /api/stacks/{node}/{stack}/draft":     {Summary: "Discard a stack's draft."},
		"POST /api/stacks/{node}/{stack}/validate":    {Summary: "Check a compose file on the node, without saving it.", Body: contentBody{}, Resp: &stacks.Validation{}},
		"GET /api/stacks/{node}/{stack}/sync":         {Summary: "A stack's git sync, or null.", Resp: syncBody{}},
		"PUT /api/stacks/{node}/{stack}/sync":         {Summary: "Follow a git branch with a stack, or change how. Some changes need a step-up.", Body: syncRequest{}, Resp: syncBody{}, Errors: []string{"invalid", "step_up_required"}},
		"DELETE /api/stacks/{node}/{stack}/sync":      {Summary: "Stop following git."},
		"POST /api/stacks/{node}/{stack}/sync/check":  {Summary: "Reach a remote and read the branch's tip, saving nothing.", Body: syncCheck{}, Resp: syncTip{}},
		"POST /api/stacks/{node}/{stack}/sync/now":    {Summary: "Check the remote now.", Status: 202, Resp: queuedBody{}},
		"POST /api/stacks/{node}/{stack}/sync/resume": {Summary: "Resume a sync that stopped.", Resp: syncBody{}},
		"GET /api/stacks/hooks":                       {Summary: "The deploy webhooks.", Resp: hookList{}},
		"POST /api/stacks/hooks":                      {Summary: "Make a deploy webhook. The secret is in this answer only.", Body: newHook{}, Status: 201, Resp: hookCreated{}, Errors: []string{"step_up_required"}},
		"POST /api/stacks/hooks/{id}/rotate":          {Summary: "Give a webhook a new secret.", Resp: hookRotated{}},
		"DELETE /api/stacks/hooks/{id}":               {Summary: "Remove a deploy webhook."},
		"POST /api/hooks/{id}": {Summary: "A forge's push webhook, verified by its signature; the body is the forge's own.", BodyMedia: "*/*", Status: 202, Resp: hookStatus{},
			Errors: []string{"too_large"}},
		"POST /api/stacks/{node}/{stack}/move/preview": {Summary: "Preview moving a stack to this node: the path rewrites, saving nothing.", Body: moveRequest{}, Resp: stacks.MovePreview{}},
	})
}
