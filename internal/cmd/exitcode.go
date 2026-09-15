package cmd

import "errors"

// Exit codes follow Unix / BSD sysexits conventions.
// See sysexits.h for the canonical reference.
const (
	ExitOK          = 0
	ExitGeneral     = 1
	ExitUsage       = 2  // EX_USAGE: invalid flags or arguments
	ExitData        = 65 // EX_DATAERR: rejected query data
	ExitNoInput     = 66 // EX_NOINPUT: input file/topic not found
	ExitUnavailable = 69 // EX_UNAVAILABLE: required service unavailable
	ExitConfig      = 78 // EX_CONFIG: configuration error
)

// ExitError carries an explicit process exit code alongside an error so the
// program can return distinct codes for distinct failure classes.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *ExitError) Unwrap() error { return e.Err }

// WithExit attaches an exit code to err. Returns nil if err is nil.
func WithExit(code int, err error) error {
	if err == nil {
		return nil
	}
	return &ExitError{Code: code, Err: err}
}

// ExitCode walks the error chain for an *ExitError and returns its code, or
// ExitGeneral for any non-nil error without a code. Returns ExitOK on nil.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var e *ExitError
	if errors.As(err, &e) {
		return e.Code
	}
	return ExitGeneral
}
