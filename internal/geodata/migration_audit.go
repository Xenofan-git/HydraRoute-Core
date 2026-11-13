package geodata

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// AuditLegacyAssets checks whether the legacy Xray asset directory contains
// files that would disappear from Xray's asset search path after selecting a
// managed generation. It never writes to either directory. Migration must
// stop if Extra is non-empty until the operator handles these dependencies.
type LegacyAssetAudit struct {
	Extra []string `json:"extra"`
}

func AuditLegacyAssets(source string) (LegacyAssetAudit, error) {
	result := LegacyAssetAudit{}
	if source == "" || !filepath.IsAbs(source) {
		return result, errors.New("absolute legacy asset directory required")
	}
	info, err := os.Lstat(source)
	if err != nil {
		return result, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0002 != 0 {
		return result, errors.New("unsafe legacy asset directory")
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "geoip.dat" || name == "geosite.dat" {
			if !entry.Type().IsRegular() {
				return result, fmt.Errorf("unsafe legacy asset %s", name)
			}
			continue
		}
		// Even hidden or non-regular entries are dependencies that cannot be
		// silently discarded. Do not follow symlinks or inspect their targets.
		result.Extra = append(result.Extra, name)
	}
	sort.Strings(result.Extra)
	for _, name := range []string{"geoip.dat", "geosite.dat"} {
		if _, err := readCandidate(filepath.Join(source, name)); err != nil {
			return result, fmt.Errorf("legacy %s: %w", name, err)
		}
	}
	return result, nil
}
