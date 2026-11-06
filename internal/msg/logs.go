package msg

// Live logs: a container's or a stack's, streamed (internal/runtime,
// internal/stacks).
func init() {
	register(map[string]string{
		"logs.follow_limit": "Following stops after 30 minutes. Follow again to carry on.",
		"logs.bad_tail":     "tail is a number of lines, or all.",
	})
}
