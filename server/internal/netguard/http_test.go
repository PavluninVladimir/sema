package netguard

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"testing"
)

type fakeResolver struct {
	ips   []netip.Addr
	calls int
}

func (r *fakeResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	r.calls++
	return r.ips, nil
}

func TestPrivateAddressesNeverDial(t *testing.T) {
	for _, addr := range []string{"127.0.0.1", "10.0.0.1", "172.17.0.1", "192.168.2.1", "169.254.169.254", "100.100.100.200", "0.0.0.0", "::1", "::ffff:127.0.0.1", "fc00::1", "fe80::1", "64:ff9b::7f00:1"} {
		for _, mixed := range []bool{false, true} {
			r := &fakeResolver{ips: []netip.Addr{netip.MustParseAddr(addr)}}
			if mixed {
				r.ips = append([]netip.Addr{netip.MustParseAddr("8.8.8.8")}, r.ips...)
			}
			dial := publicDial(r, func(context.Context, string, string) (net.Conn, error) { t.Fatalf("dialed %s", addr); return nil, nil })
			if _, err := dial(context.Background(), "tcp", "example.test:443"); err == nil {
				t.Errorf("accepted %s", addr)
			}
		}
	}
}

func TestDNSResultIsPinned(t *testing.T) {
	r := &fakeResolver{ips: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}
	dial := publicDial(r, func(_ context.Context, _, address string) (net.Conn, error) {
		if address != "8.8.8.8:443" {
			t.Fatalf("unvalidated destination: %s", address)
		}
		return nil, fmt.Errorf("test dial")
	})
	_, _ = dial(context.Background(), "tcp", "rebind.test:443")
	if r.calls != 1 {
		t.Fatalf("lookups: %d", r.calls)
	}
}

func TestURLAndRedirectPolicy(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "http://localhost/x", "http://127.0.0.1/x", "https://user:secret@example.com/x", "http://printer.local"} {
		u, _ := url.Parse(raw)
		if ValidateURL(u) == nil {
			t.Errorf("allowed %s", raw)
		}
	}
	first, _ := http.NewRequest("GET", "https://example.com/start", nil)
	for _, raw := range []string{"http://example.com/x", "https://other.example/x", "https://example.com:8443/x", "http://127.0.0.1/x"} {
		next, _ := http.NewRequest("GET", raw, nil)
		if SameOriginRedirect(next, []*http.Request{first}) == nil {
			t.Errorf("redirect %s", raw)
		}
	}
	next, _ := http.NewRequest("GET", "https://example.com/final", nil)
	if err := SameOriginRedirect(next, []*http.Request{first}); err != nil {
		t.Fatal(err)
	}
}
