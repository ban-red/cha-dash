//go:build unix

package main

import "syscall"

// privateUmask sets the process umask to 077, so the identity files and
// anything else the node creates are private to its user.
func privateUmask() { syscall.Umask(0o077) }
