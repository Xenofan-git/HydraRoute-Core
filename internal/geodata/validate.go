// Package geodata validates Xray GeoIP and GeoSite database candidates before activation.
package geodata

import (
	"errors"
	"fmt"
	"google.golang.org/protobuf/encoding/protowire"
	"strings"
)

const MaxBytes = 128 << 20

// Validate verifies a bounded protobuf database with at least one meaningful entry.
// It rejects truncated wire fields, malformed nesting and empty lists.
func Validate(name string, data []byte) error {
	if name != "geoip.dat" && name != "geosite.dat" {
		return errors.New("unsupported geodata asset")
	}
	if len(data) == 0 || len(data) > MaxBytes {
		return errors.New("invalid geodata size")
	}
	count := 0
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return protowire.ParseError(n)
		}
		data = data[n:]
		if num == 1 && typ == protowire.BytesType {
			entry, m := protowire.ConsumeBytes(data)
			if m < 0 {
				return protowire.ParseError(m)
			}
			if err := validateEntry(name, entry); err != nil {
				return err
			}
			count++
			data = data[m:]
		} else {
			m := protowire.ConsumeFieldValue(num, typ, data)
			if m < 0 {
				return protowire.ParseError(m)
			}
			data = data[m:]
		}
	}
	if count == 0 {
		return errors.New("empty geodata list")
	}
	return nil
}
func validateEntry(name string, data []byte) error {
	code := ""
	items := 0
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return protowire.ParseError(n)
		}
		data = data[n:]
		if num == 1 && typ == protowire.BytesType {
			b, m := protowire.ConsumeBytes(data)
			if m < 0 {
				return protowire.ParseError(m)
			}
			code = string(b)
			data = data[m:]
		} else if num == 2 && typ == protowire.BytesType {
			b, m := protowire.ConsumeBytes(data)
			if m < 0 {
				return protowire.ParseError(m)
			}
			if len(b) == 0 {
				return errors.New("empty geodata item")
			}
			if err := validateWireMessage(b); err != nil {
				return fmt.Errorf("invalid nested item: %w", err)
			}
			items++
			data = data[m:]
		} else {
			m := protowire.ConsumeFieldValue(num, typ, data)
			if m < 0 {
				return protowire.ParseError(m)
			}
			data = data[m:]
		}
	}
	if strings.TrimSpace(code) == "" || items == 0 {
		return fmt.Errorf("%s entry missing code or items", name)
	}
	return nil
}
func validateWireMessage(data []byte) error {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return protowire.ParseError(n)
		}
		data = data[n:]
		m := protowire.ConsumeFieldValue(num, typ, data)
		if m < 0 {
			return protowire.ParseError(m)
		}
		data = data[m:]
	}
	return nil
}
