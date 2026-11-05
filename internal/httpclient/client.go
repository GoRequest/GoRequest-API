package httpclient

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"time"
)

// New returns a reusable client restricted to public network destinations.
// Proxy environment variables are deliberately ignored so all connections go
// through the destination checks. Redirects are returned without following them.
func New() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = dialPublic
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func dialPublic(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("destination has no IP addresses")
	}
	// Check every result, then dial a checked IP directly to prevent DNS rebinding.
	for _, ip := range addresses {
		if !isPublic(ip) {
			return nil, fmt.Errorf("destination must resolve to public IP addresses")
		}
	}
	dialer := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	var dialErr error
	for _, ip := range addresses {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		dialErr = err
	}
	return nil, dialErr
}

var reservedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
}

func isPublic(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	// Allow only ordinary IPv6 global unicast addresses, excluding translation
	// prefixes that could embed an otherwise blocked IPv4 destination.
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, prefix := range reservedNetworks {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}
