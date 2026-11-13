package blocklist

import (
	"context"
	"testing"
)

func TestFetchRejectsUnsafeTargetsBeforeDial(t *testing.T) {
	for _, rawURL := range []string{
		"ftp://example.com/list.txt",
		"http://127.0.0.1/list.txt",
		"https://localhost/list.txt",
		"http://192.168.1.1/list.txt",
	} {
		t.Run(rawURL, func(t *testing.T) {
			if _, err := Fetch(context.Background(), rawURL); err == nil {
				t.Fatalf("Fetch(%q) unexpectedly succeeded", rawURL)
			}
		})
	}
}

func TestFetchHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Fetch(ctx, "https://example.com/list.txt"); err == nil {
		t.Fatal("expected canceled fetch to fail")
	}
}
