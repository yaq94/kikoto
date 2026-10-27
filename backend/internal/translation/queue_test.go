package translation

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func queueTestRun(fn func()) bool {
	go fn()
	return true
}

func waitQueueState(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A viewer's click must never wait behind a queued prefill pass, even when the
// background job was enqueued first.
func TestQueueInteractivePreemptsBackground(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := NewQueue(ctx, Limits{Concurrency: 1, QueueLimit: 8}, queueTestRun)
	release := make(chan struct{})
	order := make(chan string, 8)
	job := func(name string) func(context.Context) (Result, error) {
		return func(context.Context) (Result, error) {
			order <- name
			<-release
			return Result{Text: name}, nil
		}
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = q.DoBackground(ctx, "bg-1", 5*time.Second, job("bg-1")) }()
	if first := <-order; first != "bg-1" {
		t.Fatalf("first job = %s", first)
	}
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = q.DoBackground(ctx, "bg-2", 5*time.Second, job("bg-2")) }()
	waitQueueState(t, "queued background job", func() bool { return q.Stats().Background == 1 })
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = q.Do(ctx, "ui-1", 5*time.Second, false, job("ui-1")) }()
	waitQueueState(t, "queued interactive job", func() bool { return q.Stats().Queued == 1 })
	close(release)
	if next := <-order; next != "ui-1" {
		t.Fatalf("interactive job did not preempt the background lane: next = %s", next)
	}
	wg.Wait()
}

// The daytime window leaves two slots for interactive work; the night window
// lets the prefill pass use the full configured concurrency.
func TestQueueBackgroundConcurrencyFollowsShanghaiWindow(t *testing.T) {
	cases := []struct {
		name string
		hour int
		want int32
	}{
		{"midnight", 0, 8},
		{"night", 2, 8},
		{"before eight", 7, 8},
		{"eight", 8, 2},
		{"noon", 12, 2},
		{"late evening", 23, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			q := NewQueue(ctx, Limits{Concurrency: 8, QueueLimit: 64}, queueTestRun)
			q.Now = func() time.Time {
				return time.Date(2026, 10, 21, tc.hour, 0, 0, 0, time.FixedZone("CST", 8*3600))
			}
			release := make(chan struct{})
			var active, peak atomic.Int32
			var wg sync.WaitGroup
			for i := range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, _ = q.DoBackground(ctx, fmt.Sprintf("job-%d", i), 5*time.Second, func(context.Context) (Result, error) {
						current := active.Add(1)
						for {
							seen := peak.Load()
							if current <= seen || peak.CompareAndSwap(seen, current) {
								break
							}
						}
						<-release
						active.Add(-1)
						return Result{}, nil
					})
				}()
			}
			waitQueueState(t, "background lane to fill", func() bool { return q.Stats().Active == int(tc.want) })
			time.Sleep(50 * time.Millisecond)
			if got := q.Stats().Active; got != int(tc.want) {
				t.Fatalf("active background jobs = %d, want %d", got, tc.want)
			}
			if got := peak.Load(); got != tc.want {
				t.Fatalf("peak background concurrency = %d, want %d", got, tc.want)
			}
			close(release)
			wg.Wait()
		})
	}
}

func TestQueueMergesInFlightCallsByKey(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := NewQueue(ctx, Limits{Concurrency: 2, QueueLimit: 8}, queueTestRun)
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	var calls atomic.Int32
	fn := func(context.Context) (Result, error) {
		calls.Add(1)
		started <- struct{}{}
		<-release
		return Result{Text: "shared"}, nil
	}
	first := make(chan string, 1)
	go func() {
		result, _ := q.Do(ctx, "same-key", 5*time.Second, false, fn)
		first <- result.Text
	}()
	<-started
	second := make(chan string, 1)
	go func() {
		result, _ := q.Do(ctx, "same-key", 5*time.Second, false, fn)
		second <- result.Text
	}()
	waitQueueState(t, "second reader", func() bool { return q.Stats().Readers == 2 })
	close(release)
	if got := <-first; got != "shared" {
		t.Fatalf("first reader = %q", got)
	}
	if got := <-second; got != "shared" {
		t.Fatalf("second reader = %q", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("duplicate key generated %d calls", calls.Load())
	}
}

func TestQueueTimeoutIncludesQueueWait(t *testing.T) {
	ctx := context.Background()
	q := NewQueue(ctx, Limits{Concurrency: 2, QueueLimit: 8}, queueTestRun)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	// The job ignores its context so only the caller's deadline can settle the
	// request; that is the queue's own 60-second budget including wait time.
	_, err := q.Do(ctx, "slow", 40*time.Millisecond, false, func(context.Context) (Result, error) {
		<-release
		return Result{}, nil
	})
	if err == nil {
		t.Fatal("timed out call returned no error")
	}
	if code := PublicError(err).Code; code != "translation_timeout" {
		t.Fatalf("timeout code = %q", code)
	}
}

func TestQueueRejectsWhenTheBackgroundLaneIsFull(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := NewQueue(ctx, Limits{Concurrency: 1, QueueLimit: 1}, queueTestRun)
	release := make(chan struct{})
	blocked := func(context.Context) (Result, error) {
		<-release
		return Result{}, nil
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = q.DoBackground(ctx, "bg-1", 5*time.Second, blocked) }()
	waitQueueState(t, "first background job", func() bool { return q.Stats().Active == 1 })
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = q.DoBackground(ctx, "bg-2", 5*time.Second, blocked) }()
	waitQueueState(t, "second background job", func() bool { return q.Stats().Background == 1 })
	if _, err := q.DoBackground(ctx, "bg-3", 5*time.Second, blocked); err == nil || PublicError(err).Code != "translation_queue_full" {
		t.Fatalf("full background lane error = %v", err)
	}
	// A full background lane must not reject the interactive path.
	uiDone := make(chan error, 1)
	go func() {
		_, err := q.Do(ctx, "ui-1", 5*time.Second, false, func(context.Context) (Result, error) {
			return Result{Text: "ok"}, nil
		})
		uiDone <- err
	}()
	waitQueueState(t, "queued interactive job", func() bool { return q.Stats().Queued == 1 })
	close(release)
	wg.Wait()
	if err := <-uiDone; err != nil {
		t.Fatalf("interactive call failed on a full background lane: %v", err)
	}
}

// The interactive lane has its own 256-slot bound, independent of the
// configurable background queue. Fill it exactly and prove the next click is
// refused with the code a caller can retry on.
func TestQueueRejectsWhenTheInteractiveLaneIsFull(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := NewQueue(ctx, Limits{Concurrency: 1, QueueLimit: 8}, queueTestRun)
	release := make(chan struct{})
	blocked := func(context.Context) (Result, error) {
		<-release
		return Result{Text: "ok"}, nil
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = q.Do(ctx, "running", 10*time.Second, false, blocked) }()
	waitQueueState(t, "the single running slot", func() bool { return q.Stats().Active == 1 })
	for i := 0; i < interactiveQueueLimit; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = q.Do(ctx, fmt.Sprintf("ui-%d", i), 10*time.Second, false, blocked)
		}(i)
	}
	waitQueueState(t, "the interactive lane to fill", func() bool {
		return q.Stats().Queued == interactiveQueueLimit
	})
	if _, err := q.Do(ctx, "overflow", 10*time.Second, false, blocked); err == nil {
		t.Fatal("a request beyond the interactive bound was admitted")
	} else if code := PublicError(err).Code; code != "translation_queue_full" {
		t.Fatalf("overflow code = %q", code)
	}
	close(release)
	wg.Wait()
}

// A prefill pass fills the configured slots for hours. A trial is interactive
// work, so it must wait for one slot instead of reporting that a translation
// is already running forever.
func TestQueueTrialWaitsForBackgroundInsteadOfReportingBusy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := NewQueue(ctx, Limits{Concurrency: 1, QueueLimit: 8}, queueTestRun)
	release := make(chan struct{})
	backgroundStarted := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = q.DoBackground(ctx, "prefill", 10*time.Second, func(context.Context) (Result, error) {
			close(backgroundStarted)
			<-release
			return Result{Text: "prefill"}, nil
		})
	}()
	<-backgroundStarted
	waitQueueState(t, "the background call to hold the only slot", func() bool { return q.Stats().Active == 1 })

	trialDone := make(chan error, 1)
	go func() {
		_, err := q.Do(ctx, "trial", 10*time.Second, true, func(context.Context) (Result, error) {
			return Result{Text: "trial"}, nil
		})
		trialDone <- err
	}()
	waitQueueState(t, "the trial to wait for a slot", func() bool { return q.Stats().Queued == 1 })
	close(release)
	wg.Wait()
	if err := <-trialDone; err != nil {
		t.Fatalf("trial was refused while only background work ran: %v", err)
	}
}

// Two trials still conflict: the second must be refused rather than queued
// behind the first, because a trial is a one-shot operator action.
func TestQueueRefusesASecondTrial(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := NewQueue(ctx, Limits{Concurrency: 4, QueueLimit: 8}, queueTestRun)
	release := make(chan struct{})
	started := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = q.Do(ctx, "trial-1", 10*time.Second, true, func(context.Context) (Result, error) {
			close(started)
			<-release
			return Result{Text: "one"}, nil
		})
	}()
	<-started
	if _, err := q.Do(ctx, "trial-2", 10*time.Second, true, func(context.Context) (Result, error) {
		return Result{Text: "two"}, nil
	}); err == nil || PublicError(err).Code != "translation_busy" {
		t.Fatalf("second trial error = %v", err)
	}
	close(release)
	wg.Wait()
}
