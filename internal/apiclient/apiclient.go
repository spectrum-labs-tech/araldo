// SPDX-License-Identifier: AGPL-3.0-or-later

// Package apiclient calls an Araldo's /v1 API with a credential (an API key
// or a user token). The MCP server and the CLI's client commands use it
// (ADR 0020, ADR 0028); responses come back as the API's JSON.
package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client calls one Araldo.
type Client struct {
	// BaseURL is the install's URL, e.g. https://araldo.example.com.
	BaseURL string
	// Token is the credential, sent as a bearer token.
	Token string
	// UserAgent names the caller, e.g. araldo-cli/1.2.3.
	UserAgent string
	// Org names the org a user token acts in (the Araldo-Org header, ADR
	// 0028): an ID or name. API keys belong to one org and ignore it.
	Org  string
	HTTP *http.Client
}

// New returns a client for baseURL.
func New(baseURL, token, userAgent string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, UserAgent: userAgent,
		HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// APIError is an error response: the problem details, as the API sent
// them.
type APIError struct {
	Status  int
	Problem json.RawMessage
}

// Code is the problem's machine-readable code, if it has one.
func (e *APIError) Code() string {
	var p struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(e.Problem, &p)
	return p.Code
}

func (e *APIError) Error() string {
	var p struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	if json.Unmarshal(e.Problem, &p) == nil && p.Detail != "" {
		if p.Code != "" {
			return fmt.Sprintf("%s (HTTP %d, %s)", p.Detail, e.Status, p.Code)
		}
		return fmt.Sprintf("%s (HTTP %d)", p.Detail, e.Status)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Problem)
}

// Do sends a request and returns the response body. body is encoded as
// JSON when not nil; idemKey, when set, is sent as the Idempotency-Key.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body any, idemKey string) (json.RawMessage, error) {
	u := c.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if c.Org != "" {
		req.Header.Set("Araldo-Org", c.Org)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	resp, err := c.HTTP.Do(req) //nolint:gosec // G704: the user chooses the Araldo to call
	if err != nil {
		return nil, fmt.Errorf("calling Araldo: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("reading Araldo's answer: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		if !json.Valid(raw) {
			raw, _ = json.Marshal(map[string]string{"detail": strings.TrimSpace(string(raw))})
		}
		return nil, &APIError{Status: resp.StatusCode, Problem: raw}
	}
	return raw, nil
}
