// SPDX-License-Identifier: AGPL-3.0-or-later

// Package clientip finds the address a request came from, the same way
// for the dashboard and the API.
package clientip

import (
	"net"
	"net/http"
	"strings"
)

// From is r's client address. With header, a trusted proxy's header
// (ARALDO_CLIENT_IP_HEADER), it is the header's last entry: the address
// the proxy itself saw, which a client cannot forge by sending the header
// too (entries before it are the client's own claims). Otherwise it is the
// connection's address.
func From(r *http.Request, header string) string {
	if header != "" {
		if v := r.Header.Get(header); v != "" {
			return strings.TrimSpace(v[strings.LastIndex(v, ",")+1:])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
