package msg

// Correlation suggestions and decisions (internal/correlate).
func init() {
	register(map[string]string{
		"correlate.not_found":    "no such suggestion, decision or resource",
		"correlate.bad_decision": `a decision needs two different resources, a type (same_as, runs_on, routes_to, stores_on, depends_on, monitors) and a verdict ("confirm" or "reject")`,
	})
}
