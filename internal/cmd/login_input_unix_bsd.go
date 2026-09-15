//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package cmd

import "golang.org/x/sys/unix"

func getLoginPasscodeTermios(fd int) (*unix.Termios, error) {
	return unix.IoctlGetTermios(fd, unix.TIOCGETA)
}

func setLoginPasscodeTermios(fd int, state *unix.Termios) error {
	return unix.IoctlSetTermios(fd, unix.TIOCSETA, state)
}
