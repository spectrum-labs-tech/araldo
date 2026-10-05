// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
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
	"github.com/spectrum-labs-tech/araldo/internal/store"
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

// decode reads a JSON body strictly: unknown fields are an error, so typos
// are caught (ADR 0005).
func decode(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return badRequest("body_unreadable", "", "Could not read the request body.")
	}
	if len(body) > maxBody {
		return badRequest("body_too_large", "", "Request bodies are limited to 1 MiB.")
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		body = []byte("{}")
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var ute *json.UnmarshalTypeError
		switch {
		case errors.As(err, &ute):
			return badRequest("parameter_invalid", ute.Field, "%s must be %s.", ute.Field, ute.Type.String())
		case strings.HasPrefix(err.Error(), "json: unknown field "):
			field := strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)
			return badRequest("parameter_unknown", field, "Unknown parameter %q.", field)
		default:
			return badRequest("json_invalid", "", "The body is not valid JSON: %v", err)
		}
	}
	return nil
}

// list is a page of objects (ADR 0005).
type list struct {
	Object  string `json:"object"`
	Data    any    `json:"data"`
	HasMore bool   `json:"has_more"`
	URL     string `json:"url"`
}

// page reads limit, starting_after and ending_before.
func page(r *http.Request, prefix id.Prefix) (store.Page, error) {
	q := r.URL.Query()
	var p store.Page
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
