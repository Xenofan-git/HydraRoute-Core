package parser

import (
	"fmt"
	"strings"
)

// ParseSubscription accepts either a plain newline-delimited share-link body
// or a Base64-encoded body containing those links.
func ParseSubscription(source, body string) ([]ParsedServer, error) {
	if strings.TrimSpace(source) == "" {
		return nil, fmt.Errorf("subscription source is empty")
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, fmt.Errorf("subscription body is empty")
	}
	if !strings.Contains(body, "://") {
		compact := strings.Join(strings.Fields(body), "")
		decoded, err := decodeVMessBase64(compact)
		if err != nil {
			return nil, fmt.Errorf("subscription body is neither links nor valid Base64")
		}
		body = string(decoded)
	}
	links := make([]string, 0)
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, "://") {
			return nil, fmt.Errorf("subscription contains a non-link line")
		}
		links = append(links, line)
	}
	if len(links) == 0 {
		return nil, fmt.Errorf("subscription contains no share links")
	}
	return ParseLinks(source, links)
}
