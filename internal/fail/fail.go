// Package fail describes the errors a caller of Park's API can act on: a kind, which decides the HTTP status; a
// stable code; and a message for people. Every other error is internal — logged, and answered only as
// "Something went wrong".
package fail

import (
	"errors"
	"time"
)

// Kind is the class of an error the caller can act on.
type Kind int

// The kinds, with the HTTP status each answers with.
const (
	Invalid         Kind = iota + 1 // 400: the request is wrong
	Unauthenticated                 // 401: who is calling is unknown
	Forbidden                       // 403: the caller may not do this
	NotFound                        // 404: there is no such thing, or the caller cannot see it
	Conflict                        // 409: the state of things does not allow it
	RateLimited                     // 429: try again later
	Unavailable                     // 503: Park is not configured for it
)

// Error is an error the caller can act on.
type Error struct {
	Code    string
	Message string
	// RetryAfter is set for RateLimited.
	RetryAfter time.Duration
	Kind       Kind
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// New returns an error of kind with a stable code and a message for people.
func New(kind Kind, code, message string) *Error {
	return &Error{Kind: kind, Code: code, Message: message}
}

// As returns the Error in err's chain, if there is one.
func As(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}
