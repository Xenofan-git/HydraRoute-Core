package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"hydraroute.local/core/internal/api"
	"hydraroute.local/core/internal/geodata"
	hrruntime "hydraroute.local/core/internal/runtime"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:17890", "loopback API listen address")
	xray := flag.String("xray", "", "absolute path to staged Xray executable")
	config := flag.String("config", "", "absolute path for generated Xray config")
	stateDir := flag.String("state-dir", "/var/lib/hydraroute/state", "private directory for encrypted profiles and profile selections")
	restoreConfig := flag.Bool("restore-config", true, "validate and restore the last prepared config on startup (does not start Xray)")
	autoStart := flag.Bool("autostart", false, "start Xray after a saved config has been restored")
	refreshInterval := flag.Duration("refresh-interval", 0, "automatic profile refresh interval; 0 disables the scheduler")
	retryMin := flag.Duration("retry-min", 30*time.Second, "minimum automatic refresh retry delay")
	retryMax := flag.Duration("retry-max", 15*time.Minute, "maximum automatic refresh retry delay")
	flag.Parse()
	if *xray == "" || *config == "" {
		fatal(fmt.Errorf("-xray and -config are required"))
	}
	supervisor, err := hrruntime.NewSupervisor(*xray, *config)
	fatal(err)
	if *refreshInterval < 0 || (*refreshInterval > 0 && *refreshInterval < time.Minute) || *retryMin < 5*time.Second || *retryMax < *retryMin {
		fatal(fmt.Errorf("refresh interval must be zero or at least 1m; retry-min must be at least 5s and retry-max must be >= retry-min"))
	}
	if *restoreConfig {
		restoreCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		err := supervisor.Restore(restoreCtx)
		cancel()
		fatal(err)
	}
	// The CLI starts Xray before api.Server.Serve; guard this path too.
	// A pending update must never be bypassed by the earlier CLI autostart.
	if *autoStart {
		fatal(geodata.CheckStartupReady(filepath.Join(*stateDir, "geodata")))
	}
	if *autoStart && supervisor.Status().Configured {
		startCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		err := supervisor.Start(startCtx)
		cancel()
		fatal(err)
	}
	server := api.NewWithOptions(supervisor, *stateDir, api.Options{RefreshInterval: *refreshInterval, RetryMin: *retryMin, RetryMax: *retryMax, AutoStart: *autoStart})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(os.Stderr, "HydraRoute API listening on %s (loopback only)\n", *listen)
	fatal(server.Serve(ctx, *listen))
}
func fatal(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "hrapi:", err)
		os.Exit(1)
	}
}
