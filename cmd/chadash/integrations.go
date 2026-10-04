package main

// The integrations this hub includes. Each registers itself at init (see
// internal/integration); add a line here for every new one.
import (
	_ "dash.cha.sh/integrations/checks" // built-in HTTP and TCP checks
	_ "dash.cha.sh/specs"               // the bundled Tier 1 specs
)
