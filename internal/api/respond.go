// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
)

// maxBody bounds request bodies.
const maxBody = 1 << 20

// docsURL is where error codes are explained.
const docsURL = "https://github.com/spectrum-labs-tech/araldo/blob/main/docs/errors.md#"

// problem is an RFC 9457 problem detail with Araldo's extensions
// (ADR 0005).
type problem struct {
	Type      string            `json:"type"`
	Title     string            `json:"title"`
	Status    int               `json:"status"`
	Detail    string            `json:"detail,omitempty"`
	Code      string            `json:"code"`
	Param     string            `json:"param,omitempty"`
	RequestID string            `json:"request_id,omitempty"`
	DocURL    string            `json:"doc_url,omitempty"`
	Errors    []apperr.Problem  `json:"errors,omitempty"`
	Extra     map[string]string `json:"-"`
}

var kindStatus = map[apperr.Kind]int{
	apperr.KindInternal:         http.StatusInternalServerError,
	apperr.KindInvalid:          http.StatusUnprocessableEntity,
	apperr.KindBadRequest:       http.StatusBadRequest,
	apperr.KindNotFound:         http.StatusNotFound,
	apperr.KindForbidden:        http.StatusForbidden,
	apperr.KindUnauthorized:     http.StatusUnauthorized,
	apperr.KindConflict:         http.StatusConflict,
	apperr.KindRateLimited:      http.StatusTooManyRequests,
	apperr.KindUnavailable:      http.StatusServiceUnavailable,
	apperr.KindMethodNotAllowed: http.StatusMethodNotAllowed,
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	ae := apperr.As(err)
	status := kindStatus[ae.Kind]
	switch ae.Kind {
	case apperr.KindInternal:
		h.log.ErrorContext(r.Context(), "request failed", "path", r.URL.Path, "err", err)
	case apperr.KindUnavailable:
		h.log.WarnContext(r.Context(), "request failed: a dependency is unavailable", "path", r.URL.Path, "err", err)
		w.Header().Set("Retry-After", "30")
	case apperr.KindForbidden:
		// A refused action is audited like a change (ADR 0004).
		if a, ok := r.Context().Value(actorKey).(core.Actor); ok {
			h.svc.RecordDenial(r.Context(), a, first(r.Pattern, r.Method+" "+r.URL.Path), ae.Code)
		}
	}
	if logged, ok := r.Context().Value(loggedKey).(*loggedRequest); ok {
		logged.code = ae.Code
	}
	writeProblem(w, r, ae, status)
}

// WriteProblem answers r with err as a problem, as the API answers its own
// errors: for the server's answers under /v1 that no handler gives (a
// panic, the database down). It logs nothing.
func WriteProblem(w http.ResponseWriter, r *http.Request, err error) {
	ae := apperr.As(err)
	if ae.Kind == apperr.KindUnavailable {
		w.Header().Set("Retry-After", "30")
	}
	writeProblem(w, r, ae, kindStatus[ae.Kind])
}

func writeProblem(w http.ResponseWriter, r *http.Request, ae *apperr.Error, status int) {
	p := problem{Type: "about:blank", Title: http.StatusText(status), Status: status, Detail: ae.Message, Code: ae.Code, Param: ae.Param,
		RequestID: RequestID(r.Context()), Errors: ae.Problems}
	if p.Code != "" {
		p.DocURL = docsURL + p.Code
	}
	writeJSON(w, status, "application/problem+json", p)
}

func badRequest(code, param, format string, args ...any) error {
	return &apperr.Error{Kind: apperr.KindBadRequest, Code: code, Param: param, Message: fmt.Sprintf(format, args...)}
}

func writeJSON(w http.ResponseWriter, status int, contentType string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		slog.Error("encoding response", "err", err)
		http.Error(w, `{"code":"internal_error"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func ok(w http.ResponseWriter, status int, v any) { writeJSON(w, status, "application/json", v) }

var errBodyUnreadable = badRequest("body_unreadable", "", "Could not read the request body; send it again.")

// decode reads a JSON body strictly: unknown fields are an error, so typos
// are caught (ADR 0005), and so is anything after the one JSON value.
func decode(r *http.Request, v any) error {
	body, err := readBody(r, maxBody)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var ute *json.UnmarshalTypeError
		switch {
		case errors.As(err, &ute):
			return badRequest("parameter_invalid", ute.Field, "%s must be %s.", ute.Field, ute.Type.String())
		default:
			if field, ok := unknownField(err); ok {
				return badRequest("parameter_unknown", field, "Unknown parameter %q.", field)
			}
			return badRequest("json_invalid", "", "The body is not valid JSON: %v", err)
		}
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return badRequest("json_invalid", "", "The body must be one JSON object, with nothing after it.")
	}
	return nil
}

// unknownField reads the field a decoder with DisallowUnknownFields
// refused. encoding/json reports it only in its message, so the message is
// matched exactly; TestDecode notices if it changes.
func unknownField(err error) (string, bool) {
	field, ok := strings.CutPrefix(err.Error(), "json: unknown field ")
	if !ok {
		return "", false
	}
	if f, err := strconv.Unquote(field); err == nil {
		field = f
	}
	return field, true
}

// list is a page of objects (ADR 0005).
type list struct {
	Object  string `json:"object"`
	Data    any    `json:"data"`
	HasMore bool   `json:"has_more"`
	URL     string `json:"url"`
}

// page reads limit, starting_after and ending_before.
func page(r *http.Request, prefix id.Prefix) (core.Page, error) {
	q := r.URL.Query()
	var p core.Page
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			return p, badRequest("parameter_invalid", "limit", "limit must be between 1 and 100.")
		}
		p.Limit = n
	}
	parse := func(name string) (uuid.UUID, error) {
		v := q.Get(name)
		if v == "" {
			return uuid.Nil, nil
		}
		u, err := id.Parse(prefix, v)
		if err != nil {
			return uuid.Nil, badRequest("parameter_invalid", name, "%s must be a %s ID.", name, prefix)
		}
		return u, nil
	}
	var err error
	if p.StartingAfter, err = parse("starting_after"); err != nil {
		return p, err
	}
	if p.EndingBefore, err = parse("ending_before"); err != nil {
		return p, err
	}
	if p.StartingAfter != uuid.Nil && p.EndingBefore != uuid.Nil {
		return p, badRequest("parameter_conflict", "ending_before", "Use starting_after or ending_before, not both.")
	}
	return p, nil
}
