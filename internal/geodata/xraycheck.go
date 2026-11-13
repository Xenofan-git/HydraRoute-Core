package geodata

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// XrayCheck validates a candidate asset generation against the actual Xray
// configuration without starting listeners or changing live services.
type XrayCheck struct {
	Binary  string
	Config  string
	Timeout time.Duration
}

func (v XrayCheck) Check(assetDir string) error {
	if v.Binary == "" || v.Config == "" {
		return errors.New("xray binary and config required")
	}
	if !filepath.IsAbs(v.Binary) || !filepath.IsAbs(v.Config) {
		return errors.New("absolute Xray paths required")
	}
	if err := safeRoot(assetDir); err != nil {
		return err
	}
	for _, name := range []string{"geoip.dat", "geosite.dat"} {
		data, err := readCandidate(filepath.Join(assetDir, name))
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if err := Validate(name, data); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	timeout := v.Timeout
	if timeout <= 0 || timeout > 60*time.Second {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, v.Binary, "run", "-test", "-config", v.Config)
	// Replace rather than append XRAY_LOCATION_ASSET to avoid duplicate keys.
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "XRAY_LOCATION_ASSET=") {
			env = append(env, entry)
		}
	}
	cmd.Env = append(env, "XRAY_LOCATION_ASSET="+assetDir)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return fmt.Errorf("Xray candidate validation timed out: %w", ctx.Err())
	}
	if err != nil {
		return fmt.Errorf("Xray candidate rejected: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
