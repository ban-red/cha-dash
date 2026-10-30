package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/nodes"
	"dash.cha.sh/internal/runtime"
)

// dockerOnApprove is what approving a node does when its join token asked
// for it (on by default): a Docker integration reading through the node
// (endpoint node://<name>), named after it, created as the admin who
// approved and audited as theirs. A node whose endpoint some instance reads
// already gets no second one.
func dockerOnApprove(mgr *runtime.Manager, log *audit.Log, logger *slog.Logger) func(context.Context, nodes.Record, bool, audit.Actor) {
	return func(ctx context.Context, rec nodes.Record, addDocker bool, by audit.Actor) {
		if !addDocker {
			return
		}
		endpoint := "node://" + rec.Name
		for _, in := range mgr.List() {
			if ep, _ := in.Config["endpoint"].(string); in.Integration == "docker" && ep == endpoint {
				logger.Info("the approved node's Docker integration exists already", "node", rec.ID, "instance", in.ID)
				return
			}
		}
		name := rec.Name
		id, err := mgr.Create(ctx, runtime.CreateRequest{Integration: "docker", Name: name, Config: map[string]any{"endpoint": endpoint}})
		if errors.Is(err, runtime.ErrNameTaken) {
			name = rec.Name + " (node)"
			id, err = mgr.Create(ctx, runtime.CreateRequest{Integration: "docker", Name: name, Config: map[string]any{"endpoint": endpoint}})
		}
		if err != nil {
			logger.Error("adding the approved node's Docker integration failed; add it in Settings → Integrations", "node", rec.ID, "endpoint", endpoint, "err", err)
			return
		}
		p, _ := json.Marshal(map[string]any{"integration": "docker", "name": name, "endpoint": endpoint, "node": rec.ID, "via": "nodes.approve"})
		if _, err := log.Append(context.WithoutCancel(ctx), audit.Entry{Actor: by, Action: "settings.integrations.instance.create",
			Target: "integration:" + id, Tier: "T1", Params: p, Result: audit.OK}); err != nil {
			logger.Error("audit: recording the node's Docker integration failed", "err", err)
		}
		logger.Info("added the approved node's Docker integration", "node", rec.ID, "instance", id, "endpoint", endpoint)
	}
}
