package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"dash.cha.sh/internal/node"
	"dash.cha.sh/internal/nodeproto"
)

func step(emit func(json.RawMessage), label string) {
	b, _ := json.Marshal(map[string]string{"step": label})
	emit(b)
}

// recreate replaces a container with a new one from the same config and
// the image its tag points at now (pull first to update). It keeps the
// name, labels (so compose still owns it), networks and every volume,
// anonymous ones included, and starts the new container if the old one
// was running. If anything fails after the old container stopped, it's put
// back as it was.
//
// It's the plain-Docker equivalent of compose's recreate. Owned stacks
// redeploy through the stacks module instead.
func (m *Module) recreate(ctx context.Context, req json.RawMessage, emit func(json.RawMessage)) (json.RawMessage, error) {
	t, err := decodeTarget(req)
	if err != nil {
		return nil, err
	}
	res, err := m.writable(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	old := res.Container
	name := strings.TrimPrefix(old.Name, "/")
	if old.Config == nil || old.HostConfig == nil {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "Docker's inspect of %s is incomplete", name)
	}
	if old.HostConfig.AutoRemove {
		return nil, nodeproto.Errorf(nodeproto.CodeConflict, "%s removes itself when it stops (--rm), so it can't be recreated in place", name)
	}
	created, err := m.replace(ctx, old, "", t.Timeout, func(label string) { step(emit, label) })
	if err != nil {
		return nil, err
	}
	wctx, cancel := waitCtx(ctx)
	defer cancel()
	st, err := m.stateOf(wctx, created)
	if err != nil {
		return nil, err
	}
	return reply(map[string]string{"id": st["id"], "name": st["name"], "state": st["state"], "old_id": old.ID})
}

// replace recreates old from its own config, with image instead of its
// configured one when image isn't "", and returns the new container's ID.
// Once the old one has stopped it finishes (or puts the old one back) even
// if ctx ends.
func (m *Module) replace(ctx context.Context, old container.InspectResponse, image string, timeout int, step func(string)) (string, error) {
	name := strings.TrimPrefix(old.Name, "/")
	cfg, hc, nc := recreateConfig(old)
	if image != "" {
		cfg.Image = image
	}
	// Docker copied the old image's defaults into the container's config.
	// Drop them so the image the new container runs supplies its own, as
	// compose does. Without the old image to compare against, keep them.
	if img, err := m.cli.ImageInspect(ctx, old.Image); err == nil && img.Config != nil {
		withoutImageDefaults(cfg, img.Config)
	} else if err != nil {
		m.log.Warn("recreate: couldn't read the old image, so its defaults stay", "container", name, "err", err)
	}
	// compose recreates a container whose image label isn't the one its
	// file names, so a later `docker compose up -d` puts the file's image
	// back. Name the image this container really runs.
	if _, ok := cfg.Labels[composeImageLabel]; ok {
		if img, err := m.cli.ImageInspect(ctx, cfg.Image); err == nil {
			cfg.Labels = maps.Clone(cfg.Labels)
			cfg.Labels[composeImageLabel] = img.ID
		}
	}
	running := old.State != nil && old.State.Running

	// From here on, finish (or undo) even if the caller goes away.
	wctx, cancel := waitCtx(ctx)
	defer cancel()
	tmp := fmt.Sprintf("%s-chadash-old-%d", name, time.Now().Unix())
	if running {
		step("Stop " + name)
		if _, err := m.cli.ContainerStop(wctx, old.ID, client.ContainerStopOptions{Timeout: timeoutPtr(timeout)}); err != nil {
			return "", mapErr(err)
		}
	}
	undo := func(newID string) {
		if newID != "" {
			_, _ = m.cli.ContainerRemove(wctx, newID, client.ContainerRemoveOptions{Force: true})
		}
		_, _ = m.cli.ContainerRename(wctx, old.ID, client.ContainerRenameOptions{NewName: name})
		if running {
			_, _ = m.cli.ContainerStart(wctx, old.ID, client.ContainerStartOptions{})
		}
	}
	if _, err := m.cli.ContainerRename(wctx, old.ID, client.ContainerRenameOptions{NewName: tmp}); err != nil {
		undo("")
		return "", mapErr(err)
	}
	step("Create a new " + name)
	created, err := m.cli.ContainerCreate(wctx, client.ContainerCreateOptions{Name: name, Config: cfg, HostConfig: hc, NetworkingConfig: nc})
	if err != nil {
		undo("")
		return "", mapErr(err)
	}
	if running {
		step("Start the new " + name)
		if _, err := m.cli.ContainerStart(wctx, created.ID, client.ContainerStartOptions{}); err != nil {
			undo(created.ID)
			return "", mapErr(err)
		}
	}
	step("Remove the old container")
	if _, err := m.cli.ContainerRemove(wctx, old.ID, client.ContainerRemoveOptions{}); err != nil {
		// The new one runs; the old one is left stopped under tmp.
		m.log.Warn("removing the old container after a recreate failed", "container", name, "err", err)
	}
	return created.ID, nil
}

// recreateConfig derives the new container's config from the old one's.
func recreateConfig(old container.InspectResponse) (*container.Config, *container.HostConfig, *network.NetworkingConfig) {
	cfg := *old.Config
	// A hostname Docker generated is the old container's short ID.
	if len(old.ID) >= 12 && cfg.Hostname == old.ID[:12] {
		cfg.Hostname = ""
	}
	hc := *old.HostConfig
	hc.Mounts = slices.Clone(hc.Mounts)
	// Keep anonymous volumes: mount them by name, unless the config already
	// mounts something at that path.
	taken := map[string]bool{}
	for _, b := range hc.Binds {
		parts := strings.Split(b, ":")
		if len(parts) >= 2 {
			taken[parts[1]] = true
		}
	}
	for _, mt := range hc.Mounts {
		taken[mt.Target] = true
	}
	for _, mp := range old.Mounts {
		if mp.Type == mount.TypeVolume && mp.Name != "" && !taken[mp.Destination] {
			hc.Mounts = append(hc.Mounts, mount.Mount{Type: mount.TypeVolume, Source: mp.Name, Target: mp.Destination, ReadOnly: !mp.RW})
		}
	}
	nc := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{}}
	if old.NetworkSettings != nil {
		for n, e := range old.NetworkSettings.Networks {
			if e == nil {
				continue
			}
			// Configuration only: the addresses, MAC and IDs belonged to the
			// old endpoint, and its short-ID alias to the old container.
			aliases := slices.DeleteFunc(slices.Clone(e.Aliases), func(a string) bool {
				return len(old.ID) >= 12 && a == old.ID[:12]
			})
			nc.EndpointsConfig[n] = &network.EndpointSettings{
				IPAMConfig: e.IPAMConfig, Links: e.Links, Aliases: aliases,
				DriverOpts: e.DriverOpts, GwPriority: e.GwPriority,
			}
		}
	}
	return &cfg, &hc, nc
}

// composeImageLabel is the image ID compose records on a container it
// made, to tell when the service's image has changed.
const composeImageLabel = "com.docker.compose.image"

// withoutImageDefaults removes from cfg each setting that's the same as
// the image's own: its health check, entrypoint, command, user, working
// directory, stop signal and shell, and its env entries, labels, ports and
// volumes. What the container set itself stays.
func withoutImageDefaults(cfg *container.Config, img *dockerspec.DockerOCIImageConfig) {
	if cfg.Healthcheck != nil && img.Healthcheck != nil {
		a, _ := json.Marshal(cfg.Healthcheck)
		b, _ := json.Marshal(img.Healthcheck)
		if string(a) == string(b) {
			cfg.Healthcheck = nil
		}
	}
	if len(cfg.Entrypoint) > 0 && slices.Equal(cfg.Entrypoint, img.Entrypoint) {
		cfg.Entrypoint = nil
	}
	if len(cfg.Cmd) > 0 && slices.Equal(cfg.Cmd, img.Cmd) {
		cfg.Cmd = nil
	}
	if len(cfg.Shell) > 0 && slices.Equal(cfg.Shell, img.Shell) {
		cfg.Shell = nil
	}
	if cfg.User == img.User {
		cfg.User = ""
	}
	if cfg.WorkingDir == img.WorkingDir {
		cfg.WorkingDir = ""
	}
	if cfg.StopSignal == img.StopSignal {
		cfg.StopSignal = ""
	}
	cfg.Env = slices.DeleteFunc(slices.Clone(cfg.Env), func(e string) bool { return slices.Contains(img.Env, e) })
	if len(cfg.Labels) > 0 {
		labels := maps.Clone(cfg.Labels)
		for k, v := range img.Labels {
			if labels[k] == v {
				delete(labels, k)
			}
		}
		cfg.Labels = labels
	}
	if len(cfg.ExposedPorts) > 0 {
		ports := maps.Clone(cfg.ExposedPorts)
		for p := range ports {
			if _, ok := img.ExposedPorts[p.String()]; ok {
				delete(ports, p)
			}
		}
		cfg.ExposedPorts = ports
	}
	if len(cfg.Volumes) > 0 {
		vols := maps.Clone(cfg.Volumes)
		for v := range img.Volumes {
			delete(vols, v)
		}
		cfg.Volumes = vols
	}
}

type removeReq struct {
	ID string `json:"id"`
	// Volumes also removes the container's anonymous volumes. Named
	// volumes always stay.
	Volumes bool `json:"volumes,omitempty"`
	// Force removes a running container (kill, then remove).
	Force bool `json:"force,omitempty"`
}

// VolumePlan says which of a container's volumes a remove keeps and which
// it deletes, for the plan the hub shows before a remove.
func VolumePlan(mounts []container.MountPoint, removeAnonymous bool) (kept, removed []string) {
	for _, mp := range mounts {
		if mp.Type != mount.TypeVolume || mp.Name == "" {
			continue
		}
		// Docker names anonymous volumes with 64 hex digits.
		if removeAnonymous && shortID.MatchString(mp.Name) && len(mp.Name) == 64 {
			removed = append(removed, mp.Name)
		} else {
			kept = append(kept, mp.Name)
		}
	}
	return kept, removed
}

func (m *Module) remove(ctx context.Context, req json.RawMessage, _ func(json.RawMessage)) (json.RawMessage, error) {
	var r removeReq
	if err := decode(req, &r); err != nil {
		return nil, err
	}
	if !idRE.MatchString(r.ID) {
		return nil, node.Invalid("id must be a container ID or name")
	}
	res, err := m.writable(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	c := res.Container
	name := strings.TrimPrefix(c.Name, "/")
	if c.State != nil && c.State.Running && !r.Force {
		return nil, nodeproto.Errorf(nodeproto.CodeConflict, "%s is running; stop it first", name)
	}
	kept, removed := VolumePlan(c.Mounts, r.Volumes)
	if _, err := m.cli.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{RemoveVolumes: r.Volumes, Force: r.Force}); err != nil {
		return nil, mapErr(err)
	}
	// Docker's RemoveVolumes skips an anonymous volume that a recreate
	// attached by name, so delete the planned ones here. Gone already is
	// fine; one still in use by another container stays, and says so.
	deleted := removed[:0:0]
	for _, v := range removed {
		if _, err := m.cli.VolumeRemove(ctx, v, client.VolumeRemoveOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
			kept = append(kept, v)
			continue
		}
		deleted = append(deleted, v)
	}
	removed = deleted
	return reply(map[string]any{"id": c.ID, "name": name, "kept": nonNil(kept), "removed": nonNil(removed)})
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
