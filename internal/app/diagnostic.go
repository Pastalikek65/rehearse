package app

import (
	"errors"

	"github.com/Pastalikek65/rehearse/internal/state"
)

type operationDiagnostic struct {
	PublicCode string
	StateCode  string
	Operation  string
	Errno      int
}

type diagnosedOperationError struct {
	diagnostic operationDiagnostic
}

func (e diagnosedOperationError) Error() string { return e.diagnostic.PublicCode }

// withStateDiagnostic retains only a state-package diagnostic alongside the
// existing public error code. It never wraps or exposes the original error.
func withStateDiagnostic(publicCode string, cause error) error {
	if publicCode != "OPERATION_FAILED" && publicCode != "BACKUP_FAILED" {
		return code(publicCode)
	}
	stateDiagnostic, ok := state.DiagnosticFor(cause)
	if !ok || stateDiagnostic.Code == "" {
		return code(publicCode)
	}
	return diagnosedOperationError{diagnostic: operationDiagnostic{
		PublicCode: publicCode,
		StateCode:  stateDiagnostic.Code,
		Operation:  stateDiagnostic.Operation,
		Errno:      stateDiagnostic.Errno,
	}}
}

func operationDiagnosticFor(err error) (operationDiagnostic, bool) {
	var diagnosed diagnosedOperationError
	if !errors.As(err, &diagnosed) {
		return operationDiagnostic{}, false
	}
	return diagnosed.diagnostic, true
}
