package sdk

import (
	"errors"
	"fmt"
)

// Three kinds of failure, because they call for three different actions when
// a pipeline pages someone at night:
//
//	SourceError    the API failed         -> wait and retry, or check the vendor
//	FormatError  it answered, but the   -> fix the parser or the expansion;
//	               body does not parse       retrying will not help
//	TargetError  BigQuery refused       -> look at the schema or permissions
//
// A flat error forces reading the message to decide. Use errors.As:
//
//	var e *SourceError
//	if errors.As(err, &e) { ... }
var (
	// ErrSource, ErrFormat and ErrTarget allow errors.Is on the category
	// alone, when the details do not matter.
	ErrSource = errors.New("source error")
	ErrFormat = errors.New("format error")
	ErrTarget = errors.New("target error")
)

// SourceError means the source could not be reached or refused the request:
// network failure, timeout, or an HTTP status the SDK does not retry.
type SourceError struct {
	URL      string // already redacted
	Status   int    // 0 when the request never got a response
	Attempts int
	Cause    error
}

func (e *SourceError) Error() string {
	if e.Status > 0 {
		return fmt.Sprintf("source %s answered %d after %d attempt(s): %v",
			e.URL, e.Status, e.Attempts, e.Cause)
	}
	return fmt.Sprintf("source %s failed after %d attempt(s): %v", e.URL, e.Attempts, e.Cause)
}

func (e *SourceError) Unwrap() error { return e.Cause }
func (e *SourceError) Is(alvo error) bool {
	return alvo == ErrSource
}

// FormatError means the response arrived but could not be understood: a
// decode failure, a guard rejection, or an expansion that did not fit.
// There is deliberately no Format field. One was declared here, interpolated
// into the message, and never filled at any of the four sites that build this
// error -- so every format error read "formato  from ..." with a hole where the
// format should be, while the same run logged format=json on success. The wire
// format is in the extract complete line; three of the four sites are not about
// it anyway.
type FormatError struct {
	URL   string // already redacted
	Line  int    // -1 when it is not about a specific record
	Cause error
}

func (e *FormatError) Error() string {
	if e.Line >= 0 {
		return fmt.Sprintf("format error in %s, record %d: %v", e.URL, e.Line, e.Cause)
	}
	return fmt.Sprintf("format error in %s: %v", e.URL, e.Cause)
}

func (e *FormatError) Unwrap() error { return e.Cause }
func (e *FormatError) Is(alvo error) bool {
	return alvo == ErrFormat
}

// TargetError means BigQuery refused the write. Rows carries the per-row
// diagnostics the job reported, which is usually where the actual answer is.
type TargetError struct {
	Table string
	Rows  []string
	Cause error
}

func (e *TargetError) Error() string {
	if len(e.Rows) > 0 {
		return fmt.Sprintf("target %s refused: %v (%d per-row diagnostic(s))",
			e.Table, e.Cause, len(e.Rows))
	}
	return fmt.Sprintf("target %s refused: %v", e.Table, e.Cause)
}

func (e *TargetError) Unwrap() error { return e.Cause }
func (e *TargetError) Is(alvo error) bool {
	return alvo == ErrTarget
}
