package geodata

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Source pins a public HTTPS origin and the expected SHA-256 digest.
type Source struct {
	URL    string
	SHA256 string
}

// Fetch downloads a bounded candidate without following redirects or using proxies.
// It rejects local/private IP destinations, including DNS answers, to prevent SSRF.
func Fetch(ctx context.Context, source Source) ([]byte, error) {
	u, err := url.Parse(source.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("geodata source must be an absolute HTTPS URL")
	}
	if u.Port() != "" && u.Port() != "443" {
		return nil, errors.New("geodata source must use HTTPS port 443")
	}
	want, err := hex.DecodeString(strings.TrimSpace(source.SHA256))
	if err != nil || len(want) != sha256.Size {
		return nil, errors.New("geodata source requires SHA-256 digest")
	}
	dialer := &net.Dialer{Timeout: 8 * time.Second}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, DisableKeepAlives: true, MaxIdleConns: 0}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			if !ip.IP.IsGlobalUnicast() || ip.IP.IsPrivate() || ip.IP.IsLoopback() || ip.IP.IsLinkLocalUnicast() || ip.IP.IsLinkLocalMulticast() {
				return nil, errors.New("non-public geodata source address")
			}
		}
		if len(ips) == 0 {
			return nil, errors.New("geodata source has no addresses")
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("geodata redirects are disabled") }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("geodata source returned HTTP %d", res.StatusCode)
	}
	if res.ContentLength > MaxBytes {
		return nil, errors.New("geodata download exceeds limit")
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > MaxBytes {
		return nil, errors.New("geodata download has invalid size")
	}
	actual := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(actual[:]), hex.EncodeToString(want)) {
		return nil, errors.New("geodata SHA-256 mismatch")
	}
	return data, nil
}
