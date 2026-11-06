package docker

import (
	"context"

	"dash.cha.sh/internal/integration"
)

// Logs implements integration.LogStreamer: a container's log through its
// node (container.logs, docker.logs in the node's policy), each line with
// the time Docker stamped it. Only an instance that reads through
// chadash-node has logs here.
func (in *instance) Logs(ctx context.Context, rid string, opts integration.LogOptions, emit func(integration.LogLine)) error {
	m := targetRE.FindStringSubmatch(rid)
	if m == nil || m[1] != in.key || in.ep.node == "" || in.d.Nodes == nil {
		return integration.ErrNoLogs
	}
	return integration.NodeLogs(ctx, in.d.Nodes, in.ep.node, m[2], "", opts, emit)
}
