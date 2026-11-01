package msg

// Home files: export and import (internal/export).
func init() {
	register(map[string]string{
		"export.weak_passphrase":      "the passphrase needs at least 12 characters",
		"export.secret_in_plain_text": "refusing to write a Home file that holds the value of secret {secret} in plain text (it also appears in a non-secret field or setting); change that secret or that field",
		"export.no_vault":             "the vault isn't available",
		"export.yaml_required":        "yaml is required: the Home file's text",
		"export.wrong_passphrase":     "the passphrase is wrong, or the secrets bundle was changed",
		"export.bad_bundle":           "the secrets bundle is damaged or uses parameters this hub doesn't support",
	})
}
