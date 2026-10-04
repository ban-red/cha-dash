package main

import (
	"fmt"

	"dash.cha.sh/spikes/traefik/labkit"
)

// leakcheck scans dirs for every value on the private leak list, plus the
// list-free invariants. It never prints a value, only file:line and kind.
func leakcheck(valuesPath string, dirs []string) error {
	if err := labkit.SelfTest(); err != nil {
		return err
	}
	logf("self-test: the scanner found the planted canary")
	vals, err := labkit.ReadLeakList(valuesPath)
	if err != nil {
		return fmt.Errorf("leak list: %w", err)
	}
	kinds := map[string]int{}
	for _, v := range vals {
		kinds[v.Kind]++
	}
	logf("leak list: %d real values %v", len(vals), kinds)
	bad := 0
	for _, d := range dirs {
		f, err := labkit.ScanDir(d, vals)
		if err != nil {
			return err
		}
		for _, x := range f {
			logf("LEAK %s", x)
		}
		p, err := labkit.CheckInvariants(d)
		if err != nil {
			return err
		}
		for _, x := range p {
			logf("SUSPECT %s", x)
		}
		bad += len(f) + len(p)
		logf("%s: %d leaks, %d invariant problems", d, len(f), len(p))
	}
	if bad > 0 {
		return fmt.Errorf("%d problems", bad)
	}
	return nil
}
