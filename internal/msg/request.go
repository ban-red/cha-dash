package msg

// Request bodies the hub can't read (internal/httpx).
func init() {
	register(map[string]string{
		"request.not_json":          "request body must be application/json",
		"request.too_large":         "request body too large",
		"request.bad_json":          "malformed JSON request body",
		"request.not_object":        "request body must be a JSON object",
		"request.not_single_object": "request body must be a single JSON object",
		"request.bad_dry_run":       "dry_run must be 1 or 0",
		"request.unknown_field":     `unknown field "{field}"`,
	})
}
