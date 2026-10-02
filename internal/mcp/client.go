// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

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

	"github.com/spectrum-labs-tech/araldo/internal/buildinfo"
)

// Client calls an Araldo API with a key. Responses are passed through as
// JSON: the tools hand them to the agent as they are.
type Client struct {
	// BaseURL is the install's URL, e.g. https://araldo.example.com.
	BaseURL string
	Key     string
	HTTP    *http.Client
}

// NewClient returns a client for baseURL.
func NewClient(baseURL, key string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Key: key, HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// APIError is an error response: the problem details, as the API sent
// them.
type APIError struct {
	Status  int
	Problem json.RawMessage
}

func (e *APIError) Error() string {
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
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "araldo-mcp/"+buildinfo.Version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	resp, err := c.HTTP.Do(req) //nolint:gosec // G704: the operator chooses ARALDO_URL
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
