package watchlist

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"

	"hydraroute.local/core/internal/compiler"
)

func appendStringField(dst []byte, number protowire.Number, value string) []byte {
	dst = protowire.AppendTag(dst, number, protowire.BytesType)
	return protowire.AppendString(dst, value)
}

func appendVarintField(dst []byte, number protowire.Number, value uint64) []byte {
	dst = protowire.AppendTag(dst, number, protowire.VarintType)
	return protowire.AppendVarint(dst, value)
}

func appendDomain(value string, kind uint64, attrs ...string) []byte {
	var d []byte
	d = appendVarintField(d, 1, kind)
	d = appendStringField(d, 2, value)
	for _, attr := range attrs {
		var a []byte
		a = appendStringField(a, 1, attr)
		d = protowire.AppendTag(d, 3, protowire.BytesType)
		d = protowire.AppendBytes(d, a)
	}
	return d
}

func appendGeoSite(code string, domains ...[]byte) []byte {
	var site []byte
	site = appendStringField(site, 1, code)
	for _, d := range domains {
		site = protowire.AppendTag(site, 2, protowire.BytesType)
		site = protowire.AppendBytes(site, d)
	}
	return site
}

func writeGeoSiteFile(t *testing.T, dir, name string, sites ...[]byte) {
	t.Helper()
	var data []byte
	for _, site := range sites {
		data = protowire.AppendTag(data, 1, protowire.BytesType)
		data = protowire.AppendBytes(data, site)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestBuildMatchAndDumpPreserveLowestRankAndFirstDuplicate(t *testing.T) {
	dir := t.TempDir()
	writeGeoSiteFile(t, dir, "geosite.dat", appendGeoSite("TEST",
		appendDomain("example.org", 2),
		appendDomain("full.example.org", 3),
		appendDomain("substring-only", 0),
		appendDomain("regexp-only", 1),
		appendDomain("attr.example", 2, "ads"),
		appendDomain("other.example", 2),
	))
	cfg := compiler.RoutingConfig{
		Enabled: true,
		Rules: []compiler.RoutingRule{
			{Enabled: true, MatchType: "domain", Values: []string{"domain:ru"}, Action: "profile", ProfileID: "RU"},
			{Enabled: true, MatchType: "domain", Values: []string{"domain:google.ru", "domain:telegram.org", "domain:ru"}, Action: "profile", ProfileID: "HydraRoute"},
			{Enabled: true, MatchType: "domain", Values: []string{"geosite:TEST@ads"}, Action: "direct"},
			{Enabled: true, MatchType: "ip", Values: []string{"geoip:ru"}, Action: "block"},
		},
	}
	s, err := Build(cfg, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.targets) != 4 {
		t.Fatalf("targets=%#v", s.targets)
	}
	if !s.Partial() || s.ExcludedPatterns != 2 {
		t.Fatalf("non-enumerable GeoSite patterns should be disclosed: partial=%v excluded=%d", s.Partial(), s.ExcludedPatterns)
	}
	if got, ok, err := s.Match("Mail.Google.RU."); err != nil || !ok || got.Target != "RU" || got.Rank != 0 || got.Key != "ru" {
		t.Fatalf("lowest-rank suffix match = %#v, %v, %v", got, ok, err)
	}
	if got, ok, err := s.Match("www.telegram.org"); err != nil || !ok || got.Target != "HydraRoute" || got.Key != "telegram.org" {
		t.Fatalf("telegram suffix match = %#v, %v, %v", got, ok, err)
	}
	if got, ok, err := s.Match("sub.attr.example."); err != nil || !ok || got.Target != "DIRECT" || got.Key != "attr.example" {
		t.Fatalf("geosite attribute match = %#v, %v, %v", got, ok, err)
	}
	for _, name := range []string{"substring-only", "regexp-only", "other.example"} {
		if _, ok, _ := s.Match(name); ok {
			t.Errorf("non-enumerable or filtered GeoSite key %q unexpectedly matched", name)
		}
	}
	if _, ok, err := s.Match(strings.Repeat("a", 254)); err == nil || ok {
		t.Fatal("overlong MATCH name must return ERR name")
	}
	var dump bytes.Buffer
	if err := s.Dump(&dump); err != nil {
		t.Fatal(err)
	}
	text := dump.String()
	if !strings.HasPrefix(text, "T 0 RU\nT 1 HydraRoute\nT 2 DIRECT\nT 3 BLOCK\n") {
		t.Fatalf("unexpected target order:\n%s", text[:min(len(text), 300)])
	}
	if !strings.Contains(text, "K ru 0\n") || !strings.Contains(text, "K telegram.org 1\n") || !strings.Contains(text, "K attr.example 2\n") {
		t.Fatalf("dump missing expected keys:\n%s", text)
	}
	if strings.Contains(text, "K substring-only") || strings.Contains(text, "K regexp-only") || strings.Contains(text, "K other.example") {
		t.Fatalf("dump included unsupported or attribute-filtered entries:\n%s", text)
	}
	if !strings.HasSuffix(text, "END 4\n") {
		t.Fatalf("unexpected dump terminator: %q", text[len(text)-20:])
	}
}

func TestBuildExternalGeoSiteAndRejectUnsafeAssets(t *testing.T) {
	dir := t.TempDir()
	writeGeoSiteFile(t, dir, "custom-list.dat", appendGeoSite("CUSTOM", appendDomain("custom.example", 2)))
	cfg := compiler.RoutingConfig{Enabled: true, Rules: []compiler.RoutingRule{
		{Enabled: true, MatchType: "domain", Values: []string{"ext:custom-list.dat:CUSTOM"}, Action: "direct"},
	}}
	s, err := Build(cfg, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := s.Match("a.custom.example"); !ok || got.Target != "DIRECT" {
		t.Fatalf("external geosite did not match: %#v %v", got, ok)
	}
	if _, err := Build(compiler.RoutingConfig{Enabled: true, Rules: []compiler.RoutingRule{
		{Enabled: true, MatchType: "domain", Values: []string{"ext:../custom-list.dat:CUSTOM"}, Action: "direct"},
	}}, dir); err == nil {
		t.Fatal("path traversal must be rejected")
	}
	if err := os.Symlink(filepath.Join(dir, "custom-list.dat"), filepath.Join(dir, "linked.dat")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadGeoSiteKeys(filepath.Join(dir, "linked.dat"), "CUSTOM", nil); err == nil {
		t.Fatal("symlinked geodata must be rejected")
	}
}

func TestMatchTrailingDotAndEmptySnapshot(t *testing.T) {
	s := &Snapshot{entries: map[string]Entry{"example.org": {Key: "example.org", Rank: 1, Target: "DIRECT"}}}
	if got, ok, err := s.Match("EXAMPLE.ORG."); err != nil || !ok || got.Key != "example.org" {
		t.Fatalf("normalized match = %#v, %v, %v", got, ok, err)
	}
	if _, ok, err := s.Match("."); err == nil || ok {
		t.Fatal("empty normalized name must return error")
	}
}
