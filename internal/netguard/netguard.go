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
	"net/netip"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Policy says which non-public addresses a client may reach. The zero
// Policy allows none.
//
// Cloud metadata services (see metadata) are refused under every policy
// unless Nets names their exact address.
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
	a, ok := addr(ip)
	if !ok {
		return false
	}
	if metadata[a] {
		return p.names(a)
	}
	if public(a) {
		return true
	}
	for _, n := range p.Nets {
		if n.Contains(ip) {
			return true
		}
	}
	return p.All && !a.IsLinkLocalUnicast() && !a.IsLinkLocalMulticast()
}

// names reports whether Nets lists a by itself, as one address.
func (p Policy) names(a netip.Addr) bool {
	for _, n := range p.Nets {
		ones, bits := n.Mask.Size()
		if ones == bits && n.Contains(net.IP(a.AsSlice())) {
			return true
		}
	}
	return false
}

// metadata are the addresses where cloud metadata services answer with
// the machine's credentials. Some are not link-local (AWS's IPv6 one is a
// unique local address, Alibaba's is in the shared range, Azure's is
// public), so excluding link-local is not enough.
var metadata = map[netip.Addr]bool{
	netip.MustParseAddr("169.254.169.254"): true, // AWS, GCP, Azure, Oracle, DigitalOcean…
	netip.MustParseAddr("169.254.170.2"):   true, // AWS ECS task credentials
	netip.MustParseAddr("fd00:ec2::254"):   true, // AWS over IPv6
	netip.MustParseAddr("100.100.100.200"): true, // Alibaba Cloud
	netip.MustParseAddr("168.63.129.16"):   true, // Azure's platform endpoint
}

// addr converts ip, an IPv4-mapped IPv6 address to IPv4.
func addr(ip net.IP) (netip.Addr, bool) {
	a, ok := netip.AddrFromSlice(ip)
	return a.Unmap(), ok
}

// Public reports whether ip is a public unicast address.
func Public(ip net.IP) bool {
	a, ok := addr(ip)
	return ok && public(a)
}

func public(a netip.Addr) bool {
	for _, p := range nonPublic {
		if p.Contains(a) {
			return false
		}
	}
	// Addresses that carry an IPv4 address reach it: through a NAT64
	// gateway, or a 6to4 relay.
	if v4, ok := embeddedV4(a); ok {
		return public(v4)
	}
	return !metadata[a]
}

// embeddedV4 returns the IPv4 address a NAT64 (64:ff9b::/96) or 6to4
// (2002::/16) address carries.
func embeddedV4(a netip.Addr) (netip.Addr, bool) {
	b := a.As16()
	switch {
	case nat64.Contains(a):
		return netip.AddrFrom4([4]byte(b[12:16])), true
	case sixToFour.Contains(a):
		return netip.AddrFrom4([4]byte(b[2:6])), true
	}
	return netip.Addr{}, false
}

var (
	nat64     = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour = netip.MustParsePrefix("2002::/16")
)

// nonPublic are the ranges no public host has (IANA's special-purpose
// registries, RFC 6890): private, shared, loopback, link-local,
// documentation, benchmarking, reserved and multicast addresses.
var nonPublic = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8",       // "this network"
		"10.0.0.0/8",      // private
		"100.64.0.0/10",   // carrier-grade NAT, tailnets
		"127.0.0.0/8",     // loopback
		"169.254.0.0/16",  // link-local, cloud metadata
		"172.16.0.0/12",   // private
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // documentation
		"192.88.99.0/24",  // 6to4 relay anycast
		"192.168.0.0/16",  // private
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // documentation
		"203.0.113.0/24",  // documentation
		"224.0.0.0/4",     // multicast
		"240.0.0.0/4",     // reserved, and broadcast
		"::/96",           // unspecified, loopback, IPv4-compatible
		"::ffff:0:0:0/96", // IPv4-translated
		"64:ff9b:1::/48",  // NAT64 for local use
		"100::/64",        // discard
		"2001::/23",       // IETF protocol assignments, Teredo included
		"2001:db8::/32",   // documentation
		"3fff::/20",       // documentation
		"5f00::/16",       // segment routing
		"fc00::/7",        // unique local
		"fe80::/10",       // link-local
		"fec0::/10",       // site-local, deprecated
		"ff00::/8",        // multicast
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// UploadClient is Client for requests whose bodies may take long to send,
// such as a video uploaded to a platform: no limit on a whole request,
// which each caller sets with its context, but a server that does not
// start answering within wait of the request being sent is given up on.
func UploadClient(p Policy, wait time.Duration) *http.Client {
	c := Client(p, 0)
	if t, ok := c.Transport.(*http.Transport); ok {
		t.ResponseHeaderTimeout = wait
	}
	return c
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
