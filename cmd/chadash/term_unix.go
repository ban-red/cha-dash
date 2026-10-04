//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package main

import (
	"errors"

	"golang.org/x/sys/unix"
)

// isTerminal reports whether fd is a terminal.
func isTerminal(fd int) bool {
	_, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	return err == nil
}

// readSecretLine reads one line from the terminal fd with echo off, for a
// passphrase, and puts the terminal back as it was.
func readSecretLine(fd int) (string, error) {
	old, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return "", err
	}
	t := *old
	t.Lflag &^= unix.ECHO
	t.Lflag |= unix.ICANON | unix.ISIG
	t.Iflag |= unix.ICRNL
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, &t); err != nil {
		return "", err
	}
	defer func() { _ = unix.IoctlSetTermios(fd, ioctlSetTermios, old) }()

	var line []byte
	var b [1]byte
	for {
		n, err := unix.Read(fd, b[:])
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case err != nil:
			return "", err
		case n == 0: // end of input
			if len(line) == 0 {
				return "", errors.New("no passphrase was entered")
			}
			return string(line), nil
		}
		switch b[0] {
		case '\n':
			return string(line), nil
		case '\r':
		default:
			if len(line) >= maxPassphraseLine {
				return "", errors.New("the passphrase is too long")
			}
			line = append(line, b[0])
		}
	}
}
