package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTranslationPrefillManualPauseSurvivesInFlightModelBatch(t *testing.T) {
	s, host, admin, _ := translationHTTPFixture(t)
	seedPrefillCard(t, s, 1, "Example first title")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"Example translation"},"finish_reason":"stop"}]}`)
	}))
	defer model.Close()
	configurePrefillModel(t, host, admin, model.URL)
	unblockModel := sync.OnceFunc(func() { close(release) })
	done := make(chan error, 1)
	go func() {
		_, err := s.runTranslationPrefillBatch(ctx)
		done <- err
		close(done)
	}()
	defer func() {
		unblockModel()
		cancel()
		<-done
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("prefill did not reach the model")
	}
	const prefill = "/api/settings/translation/prefill"
	paused := translationRequest(t, host, admin, http.MethodPost, prefill, `{"status":"paused"}`, http.StatusOK)
	if paused["status"] != "paused" {
		t.Fatalf("pause response status = %v", paused["status"])
	}
	unblockModel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	paused = translationRequest(t, host, admin, http.MethodGet, prefill, "", http.StatusOK)
	if paused["status"] != "paused" || paused["lastError"] != nil {
		t.Fatalf("batch changed the manual pause: status = %v, lastError = %v", paused["status"], paused["lastError"])
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("first card model calls = %d, want 3", got)
	}

	seedPrefillCard(t, s, 2, "Example second title")
	if more, err := s.runTranslationPrefillBatch(ctx); err != nil || more || calls.Load() != 3 {
		t.Fatalf("worker continued during manual pause: more = %v, err = %v, model calls = %d", more, err, calls.Load())
	}
	translationRequest(t, host, admin, http.MethodPost, prefill, `{"status":"running"}`, http.StatusOK)
	for range 3 {
		if _, err := s.runTranslationPrefillBatch(ctx); err != nil {
			t.Fatal(err)
		}
	}
	resumed := translationRequest(t, host, admin, http.MethodGet, prefill, "", http.StatusOK)
	if resumed["status"] != "complete" || calls.Load() != 6 {
		t.Fatalf("resume did not fill only the missing card: status = %v, model calls = %d", resumed["status"], calls.Load())
	}
}

func TestTranslationPrefillManualPauseSurvivesDiskCheck(t *testing.T) {
	const healthyDisk = 2 << 30
	for _, scenario := range []struct {
		name                string
		startResourcePaused bool
		available           uint64
	}{
		{name: "disk_recovers", startResourcePaused: true, available: healthyDisk},
		{name: "disk_becomes_low"},
		{name: "disk_stays_healthy", available: healthyDisk},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			s, host, admin, _ := translationHTTPFixture(t)
			var calls atomic.Int32
			model := successfulPrefillModel(t, &calls)
			defer model.Close()
			seedPrefillCard(t, s, 1, "Example first title")
			configurePrefillModel(t, host, admin, model.URL)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			const prefill = "/api/settings/translation/prefill"
			if scenario.startResourcePaused {
				s.catalogDiskSpace = func(string) (diskSpaceSnapshot, error) {
					return diskSpaceSnapshot{}, nil
				}
				if _, err := s.runTranslationPrefillBatch(ctx); err != nil {
					t.Fatal(err)
				}
				status := translationRequest(t, host, admin, http.MethodGet, prefill, "", http.StatusOK)
				if status["status"] != "paused" || status["lastError"] != "paused_low_disk" {
					t.Fatalf("disk pause setup: status = %v, lastError = %v", status["status"], status["lastError"])
				}
			}
			// Apply the operator's command while the batch waits for a disk
			// measurement. Complete the real HTTP write before returning the measurement.
			pauseDuringCheck := sync.OnceFunc(func() {
				status := translationRequest(t, host, admin, http.MethodPost, prefill, `{"status":"paused"}`, http.StatusOK)
				if status["status"] != "paused" || status["lastError"] != nil {
					t.Fatalf("manual pause response: status = %v, lastError = %v", status["status"], status["lastError"])
				}
			})
			s.catalogDiskSpace = func(string) (diskSpaceSnapshot, error) {
				pauseDuringCheck()
				return diskSpaceSnapshot{Available: scenario.available}, nil
			}
			if _, err := s.runTranslationPrefillBatch(ctx); err != nil {
				t.Fatal(err)
			}
			afterCheck := translationRequest(t, host, admin, http.MethodGet, prefill, "", http.StatusOK)
			s.catalogDiskSpace = func(string) (diskSpaceSnapshot, error) {
				return diskSpaceSnapshot{Available: healthyDisk}, nil
			}
			if _, err := s.runTranslationPrefillBatch(ctx); err != nil {
				t.Fatal(err)
			}
			afterRecovery := translationRequest(t, host, admin, http.MethodGet, prefill, "", http.StatusOK)
			if afterCheck["status"] != "paused" || afterCheck["lastError"] != nil ||
				afterRecovery["status"] != "paused" || afterRecovery["lastError"] != nil || calls.Load() != 0 {
				t.Fatalf("manual pause changed: after disk check %v/%v, after recovery %v/%v, model calls = %d",
					afterCheck["status"], afterCheck["lastError"], afterRecovery["status"], afterRecovery["lastError"], calls.Load())
			}
			translationRequest(t, host, admin, http.MethodPost, prefill, `{"status":"running"}`, http.StatusOK)
			if _, err := s.runTranslationPrefillBatch(ctx); err != nil {
				t.Fatal(err)
			}
			resumed := translationRequest(t, host, admin, http.MethodGet, prefill, "", http.StatusOK)
			if resumed["status"] != "complete" || calls.Load() != 3 {
				t.Fatalf("manual resume did not fill the missing card: status = %v, model calls = %d", resumed["status"], calls.Load())
			}
		})
	}
}
