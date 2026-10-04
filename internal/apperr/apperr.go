// SPDX-License-Identifier: AGPL-3.0-or-later

// Package apperr is the error vocabulary shared by services and the
// surfaces in front of them (API, dashboard, CLI). Services return these;
// the API turns them into problem details (ADR 0005).
package apperr

import (
	"errors"
	"fmt"
	"strings"
)

// Kind is the class of an error, which decides the HTTP status.
type Kind int

// Error kinds.
const (
	KindInternal     Kind = iota // 500
	KindInvalid                  // 422: understood, but not acceptable
	KindBadRequest               // 400: malformed
	KindNotFound                 // 404, also for other orgs' records (ADR 0004)
	KindForbidden                // 403
	KindUnauthorized             // 401
	KindConflict                 // 409
	KindRateLimited              // 429
	KindUnavailable              // 503: a dependency is down; try again
)

// Problem is one thing wrong with a request.
type Problem struct {
	Code    string `json:"code"`
	Param   string `json:"param,omitempty"`
	Message string `json:"message"`
	// Detail carries extra facts (length, limit, target) for a problem.
	Detail map[string]any `json:"detail,omitempty"`
}

// Error is a classified application error.
type Error struct {
	Kind     Kind
	Code     string
	Message  string
	Param    string
	Problems []Problem
}

func (e *Error) Error() string {
	var sb strings.Builder
	sb.WriteString(e.Message)
	for _, p := range e.Problems {
		sb.WriteString("; ")
		if p.Param != "" {
			sb.WriteString(p.Param + ": ")
		}
		sb.WriteString(p.Message)
	}
	return sb.String()
}

// Is makes errors.Is(err, ErrNotFound) work on kinds.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error) //nolint:errorlint // comparing sentinels
	return ok && t.Code == "" && t.Kind == e.Kind
}

// Sentinels for errors.Is.
var (
	ErrNotFound     = &Error{Kind: KindNotFound}
	ErrForbidden    = &Error{Kind: KindForbidden}
	ErrInvalid      = &Error{Kind: KindInvalid}
	ErrConflict     = &Error{Kind: KindConflict}
	ErrUnauthorized = &Error{Kind: KindUnauthorized}
)

// NotFound says a thing does not exist (or is not the caller's).
func NotFound(what string) *Error {
	return &Error{Kind: KindNotFound, Code: "resource_missing", Message: "No such " + what + "."}
}

// Forbidden says the caller may not do this.
func Forbidden(format string, args ...any) *Error {
	return &Error{Kind: KindForbidden, Code: "forbidden", Message: fmt.Sprintf(format, args...)}
}

// Invalid is a 422 with one problem.
func Invalid(code, param, format string, args ...any) *Error {
	msg := fmt.Sprintf(format, args...)
	return &Error{Kind: KindInvalid, Code: code, Param: param, Message: msg, Problems: []Problem{{Code: code, Param: param, Message: msg}}}
}

// Conflict is a 409.
func Conflict(code, format string, args ...any) *Error {
	return &Error{Kind: KindConflict, Code: code, Message: fmt.Sprintf(format, args...)}
}

// Problems collects validation problems; Err returns nil when there are
// none.
type Problems []Problem

// Add appends a problem.
func (ps *Problems) Add(code, param, format string, args ...any) {
	*ps = append(*ps, Problem{Code: code, Param: param, Message: fmt.Sprintf(format, args...)})
}

// Err is a 422 listing every problem, or nil.
func (ps Problems) Err(message string) error {
	if len(ps) == 0 {
		return nil
	}
	return &Error{Kind: KindInvalid, Code: ps[0].Code, Param: ps[0].Param, Message: message, Problems: ps}
}

// As returns err as an *Error: unavailable when a registered check says a
// dependency is down, else internal for unknown errors.
func As(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	for _, down := range unavailable {
		if down(err) {
			return &Error{Kind: KindUnavailable, Code: "service_unavailable",
				Message: "A service Araldo depends on is unavailable; try again shortly."}
		}
	}
	return &Error{Kind: KindInternal, Code: "internal_error", Message: "Something went wrong on our side."}
}

var unavailable []func(error) bool

// RegisterUnavailable adds a check that recognizes a dependency being down
// (the database, say) in errors that carry no Kind, so As reports them as
// unavailable instead of internal. Call it once at startup.
func RegisterUnavailable(down func(error) bool) { unavailable = append(unavailable, down) }
