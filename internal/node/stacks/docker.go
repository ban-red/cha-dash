package stacks

import "dash.cha.sh/internal/node/stacks/dockerapi"

// The Docker seam lives in dockerapi, so the fake in stackstest can
// implement it without importing this package.
type (
	Docker    = dockerapi.Docker
	Project   = dockerapi.Project
	Container = dockerapi.Container
	Event     = dockerapi.Event
	// SelfUpdater recreates the hub's container (stack.selfupdate).
	SelfUpdater = dockerapi.SelfUpdater
	// ImageRegistry answers what a tag's registry has now
	// (stack.update.check).
	ImageRegistry = dockerapi.Registry
)

// ErrUnchecked marks a preflight that couldn't reach a registry.
var ErrUnchecked = dockerapi.ErrUnchecked
