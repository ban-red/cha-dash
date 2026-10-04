package main

import (
	"strings"
	"testing"
)

// The shape printer must never show an address, even one that looks numeric.
func TestScalarMasksAddresses(t *testing.T) {
	for _, s := range []string{"192.0.2.15", "10.0.11.1", "02:00:5e:10:00:01", "2001:db8::1", "fe80::1%vtnet0", "host.lab.example"} {
		if got := scalar(s); strings.Contains(got, s) {
			t.Errorf("scalar(%q) = %q, leaks the value", s, got)
		}
	}
	for _, s := range []string{"1500", "12.5 %", "25.7.3", "up"} {
		if got := scalar(s); !strings.Contains(got, s) {
			t.Errorf("scalar(%q) = %q, want it shown", s, got)
		}
	}
}
