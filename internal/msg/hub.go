package msg

// The hub's own sentences, said by any package, and the server's
// (internal/server).
func init() {
	register(map[string]string{
		"hub.internal_error":      "internal error",
		"hub.no_endpoint":         "no such endpoint",
		"hub.cross_origin":        "cross-origin request refused",
		"hub.causes_bad_resource": "resource must name a resource.",
	})
}
