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
	"time"
)

const MaxSubscriptionBytes = 8 << 20

// FetchSubscription downloads a plain or Base64 subscription body over HTTP(S).
// It rejects private/local destinations, unsafe redirects and oversized bodies.
func FetchSubscription(ctx context.Context, rawURL string) (string, error) {
	u, err := validateSubscriptionURL(rawURL)
	if err != nil {
		return "", err
	}
	dialer := &net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:           nil,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, fmt.Errorf("invalid subscription destination")
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("subscription host lookup failed")
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("subscription host has no addresses")
			}
			for _, item := range ips {
				if !isPublicDialIP(item.IP) {
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
	if ip := net.ParseIP(u.Hostname()); ip != nil && !isPublicDialIP(ip) {
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

func isPublicDialIP(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, block := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32"} {
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
