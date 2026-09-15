//go:build aix || linux || solaris

package cmd

import "golang.org/x/sys/unix"

func getLoginPasscodeTermios(fd int) (*unix.Termios, error) {
	return unix.IoctlGetTermios(fd, unix.TCGETS)
}

func setLoginPasscodeTermios(fd int, state *unix.Termios) error {
	return unix.IoctlSetTermios(fd, unix.TCSETS, state)
}
