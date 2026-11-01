package msg

// The audit log's queries (internal/audit).
func init() {
	register(map[string]string{
		"audit.bad_result": "result must be ok, failed or refused.",
		"audit.bad_time":   "{name} must be an RFC 3339 time.",
		"audit.bad_limit":  "limit must be between 1 and {max}.",
		"audit.bad_cursor": "That cursor isn't one this endpoint gave out.",
	})
}
