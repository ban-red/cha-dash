package msg

// Stacks (internal/stacks): the compose editor, stack secrets, git syncs,
// their webhooks, and moves between nodes.
func init() {
	register(map[string]string{
		// Paths and bodies.
		"stacks.no_node":           "There's no such node.",
		"stacks.bad_stack_name":    "A stack name uses lowercase letters, digits, - and _, starting with a letter or digit.",
		"stacks.compose_too_large": "A compose file can be at most 256 KiB.",
		"stacks.draft_empty":       "The draft is empty.",
		"stacks.move_one_source":   "Send exactly one of from_path and content.",

		// Stack secrets.
		"stacks.vault_unavailable":    "The vault isn't available.",
		"stacks.bad_secret_name":      "A secret name uses letters, digits, '.', '_' and '-', up to 64, starting with a letter or digit.",
		"stacks.secret_value_missing": "Send the secret's value.",
		"stacks.secret_too_large":     "A secret can be at most 64 KiB.",
		"stacks.secret_missing":       "The secret \"{name}\" isn't set. Set it under Settings → Stacks → Secrets first.",

		// Git syncs.
		"stacks.synced":               "Edit {path} on {branch} in {url}.",
		"stacks.protected_sync":       "Cha Dash's own stack, and any stack the node protects, can't follow git.",
		"stacks.not_synced":           "This stack doesn't follow git.",
		"stacks.credential_elsewhere": "The saved credential is only sent to {host}. Give it again to test another remote.",
		"stacks.sync_not_active":      "The sync is {state}; resume it first.",
		"stacks.syncer_down":          "The syncer isn't running.",

		// Webhooks.
		"stacks.hook_unauthorized": "unauthorized",
		"stacks.hook_rate_limited": "Too many requests; try again shortly.",
		"stacks.hook_too_large":    "A webhook's body can be at most 1 MiB; the next poll will catch up.",
		"stacks.hook_unreadable":   "The body couldn't be read.",
		"stacks.hook_exists":       "There's already a hook for {branch} on that remote. Rotate its secret instead.",
		"stacks.no_hook":           "There's no such hook.",
	})
}
