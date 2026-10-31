package docker

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"dash.cha.sh/internal/integration"
)

// updatesEvery is how often an instance through a node asks it about image
// updates. The node keeps each registry's answer for 6 hours, so asking
// more often costs the registries nothing.
const updatesEvery = 15 * time.Minute

// checkImages asks the node whether each running container's image tag has
// moved in its registry (image.check), and keeps the answers by container
// ID. Only instances through a node can: the Docker API alone has no way
// to ask without pulling. A node whose policy refuses images.check gets one
// answer for every container saying so. fresh (Check now) asks at once,
// and asks the node to ask the registries again.
func (in *instance) checkImages(ctx context.Context, list []summary, fresh bool) {
	if in.ep.node == "" {
		return
	}
	missing := false
	for _, s := range list {
		if _, ok := in.updates[s.ID]; !ok && s.State == "running" {
			missing = true
			break
		}
	}
	if !fresh && !missing && !in.updatesAt.IsZero() && in.d.Now().Sub(in.updatesAt) < updatesEvery {
		return
	}
	in.updatesAt = in.d.Now()
	var req any
	if fresh {
		req = map[string]bool{"fresh": true}
	}
	res, err := in.d.Nodes.Call(ctx, in.ep.node, "image.check", req, nil)
	if code := integration.NodeErrorCode(err); code == "forbidden" || code == "not_found" {
		// not_found: a node from before image.check, which doesn't know the verb.
		why := "The node's policy doesn't allow image checks (images.check)."
		if code == "not_found" {
			why = "The node is too old to check for image updates. Update chadash-node."
		}
		off := integration.ImageUpdate{State: integration.UpdateCantCheck, Reason: why}
		in.updates = map[string]integration.ImageUpdate{}
		for _, s := range list {
			if s.State == "running" { // the node checks only running containers
				in.updates[s.ID] = off
			}
		}
		return
	}
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			in.d.Log.Warn("docker: image check failed", "err", err)
		}
		return // keep what the last check said
	}
	var out struct {
		Images []struct {
			ID string `json:"id"`
			integration.ImageUpdate
		} `json:"images"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		in.d.Log.Warn("docker: image check answered badly", "err", err)
		return
	}
	in.updates = make(map[string]integration.ImageUpdate, len(out.Images))
	for _, u := range out.Images {
		in.updates[u.ID] = u.ImageUpdate
	}
}
