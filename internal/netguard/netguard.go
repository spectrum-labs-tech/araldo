// SPDX-License-Identifier: AGPL-3.0-or-later

// Package netguard makes HTTP clients for requests to addresses tenants
// choose (webhook endpoints, Mastodon servers). They refuse to connect to
// loopback, private, link-local and other non-public addresses unless a
// Policy allows them, checked at connect time so DNS rebinding cannot get
// around it (ADR 0012).
package netguard

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Policy says which non-public addresses a client may reach. The zero
// Policy allows none.
type Policy struct {
	// All allows every non-public address except link-local ones, where
	// cloud metadata services answer: for development, and self-hosters
	// with no tenants but themselves.
	All bool
	// Nets allows these networks, link-local or not: what a self-hoster
	// with other tenants should use, naming only the hosts it needs.
	Nets []*net.IPNet
}

// ParsePolicy reads a setting: empty or "false" allows nothing, "true"
// allows All, and otherwise it is a comma-separated list of networks
// (10.0.0.0/8) or addresses (192.168.1.20).
func ParsePolicy(s string) (Policy, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Policy{}, nil
	}
	if b, err := strconv.ParseBool(s); err == nil {
		return Policy{All: b}, nil
	}
	var p Policy
	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimSpace(part)
		if !strings.Contains(part, "/") {
			ip := net.ParseIP(part)
			if ip == nil {
				return Policy{}, fmt.Errorf("%q is not true, false, an address or a network", part)
			}
			bits := 128
			if ip.To4() != nil {
				ip, bits = ip.To4(), 32
			}
			p.Nets = append(p.Nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, n, err := net.ParseCIDR(part)
		if err != nil {
			return Policy{}, fmt.Errorf("%q is not true, false, an address or a network", part)
		}
		p.Nets = append(p.Nets, n)
	}
	return p, nil
}

// Allows reports whether the policy lets a client connect to ip.
func (p Policy) Allows(ip net.IP) bool {
	if Public(ip) {
		return true
	}
	for _, n := range p.Nets {
		if n.Contains(ip) {
			return true
		}
	}
	return p.All && ip != nil && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast()
}

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
// and connects only to the addresses p allows.
func Client(p Policy, timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	dialer.Control = func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		if !p.Allows(net.ParseIP(host)) {
			return fmt.Errorf("refusing to connect to non-public address %s", host)
		}
		return nil
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
