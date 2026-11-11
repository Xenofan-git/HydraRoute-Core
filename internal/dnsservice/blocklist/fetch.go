package blocklist

import (
	"context"
	"fmt"
	"strings"

	"hydraroute.local/core/internal/parser"
)

// Fetch downloads a public HTTP(S) block source using the Core subscription
// fetcher's redirect, DNS-rebinding, destination and body-size protections,
// then parses its domain-oriented content.
func Fetch(ctx context.Context, rawURL string) (Result, error) {
	return FetchOnInterface(ctx, rawURL, "")
}

func FetchOnInterface(ctx context.Context, rawURL, interfaceName string) (Result, error) {
	body, err := parser.FetchSubscriptionOnInterface(ctx, rawURL, interfaceName)
	if err != nil {
		return Result{}, fmt.Errorf("fetch DNS blocklist: %w", err)
	}
	result, err := Parse(strings.NewReader(body))
	if err != nil {
		return Result{}, fmt.Errorf("parse DNS blocklist: %w", err)
	}
	return result, nil
}
