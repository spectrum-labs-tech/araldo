// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// maxHTTPMessage bounds a POST /v1/mcp body.
const maxHTTPMessage = 4 << 20

// HTTPHandler serves MCP over HTTP (the Streamable HTTP transport,
// stateless): each POST carries one JSON-RPC message, or a batch, and gets
// its answer as JSON; there is no session and no server-initiated stream.
// The tools run as the caller's API key, through API, the API handler
// itself, in-process: they can do what that key can and nothing more
// (ADR 0020).
type HTTPHandler struct {
	API http.Handler
}

func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// A browser page on another site must not drive the tools (DNS
	// rebinding); clients that are not browsers send no Origin.
	if o := r.Header.Get("Origin"); o != "" {
		if u, err := url.Parse(o); err != nil || u.Host != r.Host {
			writeRPCError(w, http.StatusForbidden, codeInvalidRequest, "this origin may not call the MCP endpoint")
			return
		}
	}
	if v := r.Header.Get("MCP-Protocol-Version"); v != "" && !slices.Contains(protocolVersions, v) {
		writeRPCError(w, http.StatusBadRequest, codeInvalidRequest, "unsupported MCP-Protocol-Version "+v)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxHTTPMessage+1))
	if err != nil || len(body) > maxHTTPMessage {
		writeRPCError(w, http.StatusRequestEntityTooLarge, codeInvalidRequest, "the message is too large")
		return
	}
	api := NewClient("http://araldo.internal", bearer(r))
	api.HTTP = &http.Client{Transport: inProcess{h: h.API, parent: r}}
	s := NewServer(api)

	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '[' {
		var batch []json.RawMessage
		if err := json.Unmarshal(body, &batch); err != nil || len(batch) == 0 {
			writeRPCError(w, http.StatusBadRequest, codeParse, "not a JSON-RPC message")
			return
		}
		var out []response
		for _, m := range batch {
			if res, ok := s.message(r.Context(), m); ok {
				out = append(out, res)
			}
		}
		if len(out) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	res, ok := s.message(r.Context(), body)
	switch {
	case !ok:
		w.WriteHeader(http.StatusAccepted) // a notification
	case res.Error != nil && res.Error.Code == codeParse:
		writeJSON(w, http.StatusBadRequest, res)
	default:
		writeJSON(w, http.StatusOK, res)
	}
}

// bearer is the caller's API key, as the API accepts it: a bearer token, or
// the Basic username.
func bearer(r *http.Request) string {
	if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return strings.TrimSpace(token)
	}
	if user, _, ok := r.BasicAuth(); ok {
		return user
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeRPCError(w http.ResponseWriter, status, code int, msg string) {
	writeJSON(w, status, response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: code, Message: msg}})
}

// inProcess sends the tools' API calls straight to the API handler,
// without the network. Each call already carries the outer request's
// context; it also gets the outer request's remote address.
type inProcess struct {
	h      http.Handler
	parent *http.Request
}

func (t inProcess) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	req.RemoteAddr = t.parent.RemoteAddr
	rec := &recorder{header: http.Header{}, status: http.StatusOK}
	t.h.ServeHTTP(rec, req)
	return &http.Response{StatusCode: rec.status, Status: http.StatusText(rec.status), Header: rec.header,
		Body: io.NopCloser(&rec.body), ContentLength: int64(rec.body.Len()), Request: req, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1}, nil
}

// recorder collects a response written by the API handler.
type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
	wrote  bool
}

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) WriteHeader(status int) {
	if !r.wrote {
		r.status, r.wrote = status, true
	}
}

func (r *recorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.body.Write(b)
}
