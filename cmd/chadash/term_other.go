//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package main

import "errors"

// isTerminal is false where the hub can't turn echo off: a passphrase then
// has to come from CHADASH_EXPORT_PASSPHRASE.
func isTerminal(int) bool { return false }

func readSecretLine(int) (string, error) {
	return "", errors.New("reading a passphrase from the terminal isn't supported here")
}
