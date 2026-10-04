// Package errdefs classifies engine errors so the API layer can map them to
// the HTTP status codes the Docker Engine API uses.
package errdefs

import (
	"errors"
	"fmt"
)

// Kind is the class of an error.
type Kind int

const (
	KindInternal Kind = iota
	KindNotFound
	KindInvalid
	KindConflict
	KindNotModified
	KindUnsupported
	KindForbidden
)

type kindError struct {
	kind Kind
	err  error
}

func (e *kindError) Error() string { return e.err.Error() }
func (e *kindError) Unwrap() error { return e.err }

func wrap(k Kind, format string, args ...any) error {
	return &kindError{kind: k, err: fmt.Errorf(format, args...)}
}

// NotFound reports a missing object (404).
func NotFound(format string, args ...any) error { return wrap(KindNotFound, format, args...) }

// Invalid reports a bad request parameter (400).
func Invalid(format string, args ...any) error { return wrap(KindInvalid, format, args...) }

// Conflict reports an operation that conflicts with the object's state (409).
func Conflict(format string, args ...any) error { return wrap(KindConflict, format, args...) }

// NotModified reports a no-op state change, such as starting a running
// container (304).
func NotModified(format string, args ...any) error { return wrap(KindNotModified, format, args...) }

// Unsupported reports a Docker feature ThothDock deliberately does not
// provide (501).
func Unsupported(format string, args ...any) error { return wrap(KindUnsupported, format, args...) }

// Forbidden reports a request refused by policy (403).
func Forbidden(format string, args ...any) error { return wrap(KindForbidden, format, args...) }

// KindOf returns the class of err, or KindInternal when it has none.
func KindOf(err error) Kind {
	var ke *kindError
	if errors.As(err, &ke) {
		return ke.kind
	}
	return KindInternal
}
