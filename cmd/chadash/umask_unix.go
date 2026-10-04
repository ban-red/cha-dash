//go:build unix

package main

import "syscall"

// privateUmask sets the process umask to 077, so every file the hub creates
// is private to its user, and returns the previous mask. SQLite's -shm file
// is why: the driver creates it 0666 minus the umask. The database and its
// -wal file are 0600 either way (see internal/store).
func privateUmask() int { return syscall.Umask(0o077) }
