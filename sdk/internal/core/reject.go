package core

import (
	"errors"
	"fmt"
)

// ErrRejected marks an error as "the source answered, and what it sent is not
// data". Test for it with errors.Is.
var ErrRejected = errors.New("rejected")

// Rejection is what Reject returns.
type Rejection struct {
	Reason string
}

func (r *Rejection) Error() string { return r.Reason }

// Is makes errors.Is(err, ErrRejected) work.
func (r *Rejection) Is(target error) bool { return target == ErrRejected }

// Reject refuses a response, or a record, saying why.
//
//	return nil, sdk.Reject("open-meteo refused: %v", doc["reason"])
//
// A plain fmt.Errorf also fails the run, but it cannot be told apart from a
// nil map or a typo in the fetcher -- and those two want different things
// from whoever is on call. A rejection means the vendor sent something that
// is not data: the fetcher is fine, the source is not, and retrying the same
// window will do the same thing. That is the difference the log and the alert
// have to carry, so it has a type.
func Reject(format string, args ...any) error {
	return &Rejection{Reason: fmt.Sprintf(format, args...)}
}
