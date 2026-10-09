package state

import (
	"errors"
	"syscall"
)

// Diagnostic contains only fixed state error codes, fixed operation names, and
// numeric OS error values. It deliberately carries no original error or path.
// Callers should use this for internal diagnosis, never as user-facing output.
type Diagnostic struct {
	Code      string
	Operation string
	Errno     int
}

type diagnosticCarrier interface {
	stateDiagnostic() Diagnostic
}

type operationError struct {
	code      code
	operation string
	errno     int
}

func (e operationError) Error() string { return string(e.code) }
func (e operationError) stateDiagnostic() Diagnostic {
	return Diagnostic{Code: string(e.code), Operation: e.operation, Errno: e.errno}
}

func (e code) stateDiagnostic() Diagnostic { return Diagnostic{Code: string(e)} }

// DiagnosticFor returns a bounded internal view only for errors created by the
// state package. The original error is never exposed or retained in the view.
func DiagnosticFor(err error) (Diagnostic, bool) {
	var carrier diagnosticCarrier
	if !errors.As(err, &carrier) {
		return Diagnostic{}, false
	}
	return carrier.stateDiagnostic(), true
}

func operationFailure(codeValue, operation string, cause error) error {
	switch operation {
	case "open", "write", "sync", "close", "rename":
	default:
		operation = ""
	}
	return operationError{code: code(codeValue), operation: operation, errno: numericErrno(cause)}
}

func numericErrno(err error) int {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return int(errno)
	}
	return 0
}
