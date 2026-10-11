// Package netguard ограничивает адреса, полученные от скриптов и внешних данных.
package netguard

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var reserved = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("2001::/32"),
}
var globalIPv6 = netip.MustParsePrefix("2000::/3")

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.Zone() != "" || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	// Исключаем IPv6-переходники, в том числе NAT64 к частным IPv4.
	if ip.Is6() && !globalIPv6.Contains(ip) {
		return false
	}
	for _, p := range reserved {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

func ValidateURL(u *url.URL) error {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("нужен публичный HTTP(S) адрес без учётных данных")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return fmt.Errorf("локальные адреса недоступны скриптам")
	}
	if ip, err := netip.ParseAddr(host); err == nil && !publicIP(ip) {
		return fmt.Errorf("служебные и частные адреса недоступны скриптам")
	}
	return nil
}

type resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}
type dialFunc func(context.Context, string, string) (net.Conn, error)

func publicDial(resolve resolver, dial dialFunc) dialFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := resolve.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("у адреса нет IP")
		}
		for _, ip := range ips {
			if !publicIP(ip) {
				return nil, fmt.Errorf("DNS вернул служебный или частный адрес")
			}
		}
		// Подключаемся к проверенному IP, без повторного разрешения имени.
		for _, ip := range ips {
			var conn net.Conn
			conn, err = dial(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
		}
		return nil, err
	}
}

// SameOriginRedirect не передаёт секретные заголовки другому сервису.
func SameOriginRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return fmt.Errorf("слишком много перенаправлений")
	}
	if len(via) > 0 && (req.URL.Scheme != via[0].URL.Scheme || !strings.EqualFold(req.URL.Host, via[0].URL.Host)) {
		return fmt.Errorf("перенаправление на другой источник запрещено")
	}
	return nil
}

type guardedTransport struct{ transport *http.Transport }

func (g guardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := ValidateURL(req.URL); err != nil {
		return nil, err
	}
	return g.transport.RoundTrip(req)
}

func Client(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // прокси разрешал бы имя заново, обходя проверку IP
	transport.DialContext = publicDial(net.DefaultResolver, (&net.Dialer{Timeout: 10 * time.Second}).DialContext)
	return &http.Client{Timeout: timeout, Transport: guardedTransport{transport}, CheckRedirect: SameOriginRedirect}
}
