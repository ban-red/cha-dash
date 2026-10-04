//go:build !unix

package main

// privateUmask does nothing where there is no umask.
func privateUmask() int { return 0 }
