// Package serviceerr defines transport-independent Assignment 2 errors.
package serviceerr

type Error struct {
	Status int
	Code   string
}

func (e *Error) Error() string          { return e.Code }
func New(status int, code string) error { return &Error{status, code} }
