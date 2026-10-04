// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"github.com/spectrum-labs-tech/araldo/internal/apiclient"
	"github.com/spectrum-labs-tech/araldo/internal/buildinfo"
)

// Client calls an Araldo API with a key. Responses are passed through as
// JSON: the tools hand them to the agent as they are.
type Client = apiclient.Client

// APIError is an error response: the problem details, as the API sent
// them.
type APIError = apiclient.APIError

// NewClient returns a client for baseURL.
func NewClient(baseURL, key string) *Client {
	return apiclient.New(baseURL, key, "araldo-mcp/"+buildinfo.Version)
}
