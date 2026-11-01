package msg

// The Demo Home and the public demo (internal/demo, CHADASH_DEMO_PUBLIC).
func init() {
	register(map[string]string{
		"demo.read_only":    "This is the public demo: only the Demo Home's own actions run here, so this can't be changed. Install Cha Dash to try it on your own home.",
		"demo.signin_limit": "Too many demo visits from this address. Try again in a minute.",
	})
}
