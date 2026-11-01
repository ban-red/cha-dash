package msg

// The hub's own sentences, said by any package.
func init() {
	register(map[string]string{
		"hub.internal_error": "internal error",
	})
}
