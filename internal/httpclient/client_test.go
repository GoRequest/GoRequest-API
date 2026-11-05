package httpclient

import (
	"net/http"
	"net/netip"
	"testing"
)

func TestPublicAddresses(t *testing.T) {
	for _, address := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if !isPublic(netip.MustParseAddr(address)) {
			t.Errorf("public address rejected: %s", address)
		}
	}
	for _, address := range []string{
		"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.169.254",
		"0.0.0.0", "0.1.2.3", "100.100.100.200", "198.18.0.1", "192.0.2.1", "224.0.0.1", "255.255.255.255",
		"::1", "::", "fc00::1", "fe80::1", "ff02::1", "::ffff:127.0.0.1", "64:ff9b::7f00:1",
		"2001:db8::1", "2002:7f00:1::", "2001::1",
	} {
		if isPublic(netip.MustParseAddr(address)) {
			t.Errorf("nonpublic address allowed: %s", address)
		}
	}
}

func TestClientPolicy(t *testing.T) {
	client := New()
	defer client.CloseIdleConnections()
	if client.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("proxy would bypass destination checks")
	}
	if err := client.CheckRedirect(nil, nil); err != http.ErrUseLastResponse {
		t.Fatalf("redirects should not be followed: %v", err)
	}
	for _, target := range []string{"http://127.0.0.1:1", "http://[::1]:1", "http://localhost:1"} {
		_, err := client.Get(target)
		if err == nil || !containsPolicyError(err) {
			t.Fatalf("expected blocked destination for %s, got %v", target, err)
		}
	}
}

func containsPolicyError(err error) bool {
	for err != nil {
		if err.Error() == "destination must resolve to public IP addresses" {
			return true
		}
		wrapped, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = wrapped.Unwrap()
	}
	return false
}
