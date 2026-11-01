package msg

// Nodes (internal/nodes): joining, approving and revoking nodes, join
// tokens and local pairing.
func init() {
	register(map[string]string{
		"nodes.handshakes_busy": "Too many node handshakes in progress; try again shortly.",
		"nodes.shutting_down":   "The hub is shutting down.",
		"nodes.list_failed":     "The nodes couldn't be listed.",
		"nodes.no_node":         "There's no such node.",
		"nodes.already_revoked": "That node is already revoked.",
		"nodes.revoke_failed":   "The node couldn't be revoked.",
		"nodes.not_pending":     "That node isn't waiting for approval.",
		"nodes.approve_failed":  "The node couldn't be approved.",
		"nodes.pairing_off":     "Local pairing is off on this hub (CHADASH_NODE_PAIRING_FILE).",
		"nodes.pairing_failed":  "The pairing secret couldn't be regenerated.",

		// Join tokens.
		"nodes.tokens_list_failed":  "The join tokens couldn't be listed.",
		"nodes.token_failed":        "The join token couldn't be made.",
		"nodes.no_token":            "There's no such join token.",
		"nodes.token_spent":         "That join token is already used, expired or revoked.",
		"nodes.token_revoke_failed": "The join token couldn't be revoked.",
		"nodes.token_ttl":           "A join token lives 1 minute to {hours} hours.",
		"nodes.bad_name":            "A node name is 1–63 letters, digits, dots, dashes or underscores, starting with a letter or digit.",
		"nodes.name_reserved":       "A node can't be called \"{name}\": the hub uses that name.",
		"nodes.name_local":          "A node can't join as \"{name}\": that's the hub's own node, which pairs locally.",
	})
}
