package cli

import "fmt"

// Exit codes. Documented in docs/design/01-cli.md §6 and stable: a test harness
// distinguishes "I asked for something impossible" from "it did not finish in
// time" from "CUBRID did something we have not modelled", and the third is a bug
// report rather than a retry.
const (
	ExitOK           = 0 // success
	ExitFailed       = 1 // failed for a reason the tool understands and reported
	ExitUsage        = 2 // unknown flag, bad selector, missing argument
	ExitPrecondition = 3 // the cluster is not in a state where this makes sense
	ExitTimeout      = 4 // timed out waiting for the engine to reach a state
	ExitUnexpected   = 5 // the engine reached a state the tool did not model
)

// Error carries the exit code and the machine-readable note code together, so a
// failure reaches both audiences in one value.
type Error struct {
	Code int    // one of the Exit* constants
	Note string // note code, e.g. "no_such_cluster"
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func failf(code int, note, format string, a ...any) *Error {
	return &Error{Code: code, Note: note, Msg: fmt.Sprintf(format, a...)}
}

// Usage, Precondition and Failed are the three a command reaches for most.
func Usage(format string, a ...any) *Error {
	return failf(ExitUsage, "usage", format, a...)
}
func Precondition(note, format string, a ...any) *Error {
	return failf(ExitPrecondition, note, format, a...)
}
func Failed(note, format string, a ...any) *Error {
	return failf(ExitFailed, note, format, a...)
}
