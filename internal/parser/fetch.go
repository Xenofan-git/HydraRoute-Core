package parser

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const MaxSubscriptionBytes = 8 << 20

// FetchSubscription downloads a plain or Base64 subscription body over HTTP(S).
// It rejects private/local destinations, unsafe redirects and oversized bodies.
func FetchSubscription(ctx context.Context, rawURL string) (string, error) {
	return FetchSubscriptionOnInterface(ctx, rawURL, "")
}

// FetchSubscriptionOnInterface binds both DNS lookups and HTTP(S) connections
// to the requested Linux network interface. An empty interface uses normal
// system routing.
func FetchSubscriptionOnInterface(ctx context.Context, rawURL, interfaceName string) (string, error) {
	u, err := validateSubscriptionURL(rawURL)
	if err != nil {
		return "", err
	}
	if interfaceName != "" {
		iface, err := net.InterfaceByName(interfaceName)
		if err != nil || iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			return "", fmt.Errorf("DNS blocklist interface %q is unavailable or not an active network interface", interfaceName)
		}
	}
	dialer := &net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}
	if interfaceName != "" {
		dialer.Control = func(network, address string, raw syscall.RawConn) error {
			var socketErr error
			if err := raw.Control(func(fd uintptr) {
				socketErr = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, interfaceName)
			}); err != nil {
				return err
			}
			return socketErr
		}
	}
	// Resolve through the system resolver first: on Debian this may be a
	// local stub (127.0.0.53) that cannot itself be reached through a bound
	// physical interface. The actual HTTP(S) sockets remain interface-bound.
	resolver := net.DefaultResolver
	transport := &http.Transport{
		Proxy:           nil,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, fmt.Errorf("invalid subscription destination")
			}
			ips, err := resolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("subscription host lookup failed")
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("subscription host has no addresses")
			}
			for _, item := range ips {
				if !IsPublicDialIP(item.IP) {
					return nil, fmt.Errorf("subscription host resolves to a non-public address")
				}
			}
			var lastErr error
			for _, item := range ips {
				conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(item.IP.String(), port))
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
	}
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return fmt.Errorf("too many subscription redirects")
		}
		if req.URL.Scheme != "https" && req.URL.Scheme != "http" {
			return fmt.Errorf("unsupported subscription redirect scheme")
		}
		if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https" {
			return fmt.Errorf("subscription redirect cannot downgrade HTTPS")
		}
		if _, err := validateSubscriptionURL(req.URL.String()); err != nil {
			return err
		}
		return nil
	}
	return fetchSubscriptionWithClient(ctx, u.String(), client)
}

func validateSubscriptionURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("subscription URL must be an HTTP(S) URL without userinfo or fragment")
	}
	if !validServerHost(u.Hostname()) {
		return nil, fmt.Errorf("subscription URL host is invalid")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && !IsPublicDialIP(ip) {
		return nil, fmt.Errorf("subscription URL cannot target a private or local address")
	}
	if p := u.Port(); p != "" {
		n, e := strconv.Atoi(p)
		if e != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("subscription URL port is invalid")
		}
	}
	return u, nil
}

func IsPublicDialIP(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, block := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "2001::/32", "2001:db8::/32", "2002::/16", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001:10::/28", "2001:20::/28", "2001:2::/48"} {
		_, network, _ := net.ParseCIDR(block)
		if network.Contains(ip) {
			return false
		}
	}
	return true
}

func fetchSubscriptionWithClient(ctx context.Context, rawURL string, client *http.Client) (string, error) {
	u, err := validateSubscriptionURL(rawURL)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("cannot create subscription request")
	}
	req.Header.Set("Accept", "text/plain, application/octet-stream, */*")
	req.Header.Set("User-Agent", "HydraRoute-Core/0.x")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("subscription download failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("subscription server returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxSubscriptionBytes+1))
	if err != nil {
		return "", fmt.Errorf("cannot read subscription response")
	}
	if len(body) > MaxSubscriptionBytes {
		return "", fmt.Errorf("subscription response exceeds 8 MiB limit")
	}
	if len(body) == 0 {
		return "", fmt.Errorf("subscription response is empty")
	}
	return string(body), nil
}
