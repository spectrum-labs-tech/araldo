// SPDX-License-Identifier: AGPL-3.0-or-later

// Package netguard makes HTTP clients for requests to addresses tenants
// choose (webhook endpoints, Mastodon servers). Unless private addresses
// are allowed, they refuse to connect to loopback, private, link-local and
// other non-public addresses, checked at connect time so DNS rebinding
// cannot get around it (ADR 0012).
package netguard

import (
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"
)

// Public reports whether ip is a public unicast address.
func Public(ip net.IP) bool {
	return ip != nil && !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() &&
		!ip.IsUnspecified() && !ip.IsMulticast() && !ip.IsInterfaceLocalMulticast() && !isSharedOrReserved(ip)
}

// isSharedOrReserved covers ranges net.IP has no method for: carrier-grade
// NAT (100.64.0.0/10, which includes tailnets) and 0.0.0.0/8.
func isSharedOrReserved(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil {
		return v4[0] == 0 || v4[0] == 100 && v4[1]&0xc0 == 64
	}
	return false
}

// Client returns an HTTP client with timeout. It never follows redirects,
// and with allowPrivate false it connects only to public addresses.
func Client(allowPrivate bool, timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	if !allowPrivate {
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if !Public(net.ParseIP(host)) {
				return fmt.Errorf("refusing to connect to non-public address %s", host)
			}
			return nil
		}
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: dialer.DialContext, TLSHandshakeTimeout: 5 * time.Second, Proxy: nil,
			MaxIdleConnsPerHost: 4, IdleConnTimeout: 90 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
