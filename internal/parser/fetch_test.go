package parser

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFetchSubscriptionWithClient(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.Header.Get("User-Agent") != "HydraRoute-Core/0.x" {
			t.Fatalf("request=%v headers=%v", r.Method, r.Header)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("vless://link")), Header: make(http.Header), Request: r}, nil
	})}
	body, err := fetchSubscriptionWithClient(context.Background(), "https://provider.example/sub", client)
	if err != nil || body != "vless://link" {
		t.Fatalf("body=%q err=%v", body, err)
	}
}

func TestFetchSubscriptionRejectsBadURLAndHTTPStatus(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", "ftp://provider.example/sub", "https://user:pass@provider.example/sub", "https://provider.example/sub#fragment", "https://bad_host/sub", "http://provider.example:99999/sub", "http://127.0.0.1/sub", "https://100.64.0.1/sub"} {
		if _, err := validateSubscriptionURL(u); err == nil {
			t.Errorf("expected invalid URL: %s", u)
		}
	}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader("denied")), Header: make(http.Header), Request: r}, nil
	})}
	if _, err := fetchSubscriptionWithClient(context.Background(), "https://provider.example/sub", client); err == nil {
		t.Fatal("expected HTTP status error")
	}
}

func TestFetchSubscriptionEnforcesSizeLimit(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", MaxSubscriptionBytes+1))), Header: make(http.Header), Request: r}, nil
	})}
	if _, err := fetchSubscriptionWithClient(context.Background(), "https://provider.example/sub", client); err == nil {
		t.Fatal("expected size limit error")
	}
}

func TestPublicIPGuard(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "192.168.1.1", "169.254.1.1", "::1", "fc00::1", "fe80::1", "100.64.0.1", "192.0.2.1", "2001:db8::1", "64:ff9b::7f00:1", "64:ff9b:1::a00:1", "2002:7f00:1::", "2001::1", "2001:20::1", "2001:2::1", "192.88.99.1", "0.1.2.3"} {
		if IsPublicDialIP(net.ParseIP(ip)) {
			t.Errorf("private IP accepted: %s", ip)
		}
	}
	if !IsPublicDialIP(net.ParseIP("1.1.1.1")) {
		t.Fatal(fmt.Sprint("public IP rejected"))
	}
}

func TestFetchSubscriptionOnInterfaceRejectsMissingInterfaceBeforeNetwork(t *testing.T) {
	_, err := FetchSubscriptionOnInterface(context.Background(), "https://example.com/list.txt", "hrcore-no-such-interface")
	if err == nil || !strings.Contains(err.Error(), "interface") {
		t.Fatalf("missing interface was not rejected before download: %v", err)
	}
}
