package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestSimulatedLTEOutageRecovery uses a local upstream to model LTE data-plane
// loss and restoration. It never changes the host network, routes, or modem.
// The process recovery loop must retry failed upstream checks and recover
// automatically once connectivity returns.
func TestSimulatedLTEOutageRecovery(t *testing.T) {
	var linkUp atomic.Bool
	linkUp.Store(true)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !linkUp.Load() {
			http.Error(w, "simulated LTE link down", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: time.Second}).DialContext}}
	check := func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream.URL, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			return fmt.Errorf("upstream unavailable: %d", resp.StatusCode)
		}
		return nil
	}
	if err := check(context.Background()); err != nil {
		t.Fatal(err)
	}
	linkUp.Store(false)
	if err := check(context.Background()); err == nil {
		t.Fatal("LTE outage was not detected")
	}
	h := NewWithStateDir(apiSupervisor(t), t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var attempts atomic.Int32
	recovered := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.runRecoveryLoop(ctx, "runtime", func(c context.Context) error {
			attempts.Add(1)
			err := check(c)
			if err == nil {
				select {
				case <-recovered:
				default:
					close(recovered)
				}
			}
			return err
		})
	}()
	deadline := time.After(12 * time.Second)
	for attempts.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("recovery did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	linkUp.Store(true)
	select {
	case <-recovered:
	case <-deadline:
		t.Fatal("recovery did not resume after LTE restoration")
	}
	cancel()
	<-done
	if attempts.Load() < 2 {
		t.Fatalf("expected retry after outage, got %d", attempts.Load())
	}
}
