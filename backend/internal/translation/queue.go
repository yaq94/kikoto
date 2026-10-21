package translation

import (
	"context"
	"sync"
	"time"
	_ "time/tzdata"
)

// The interactive lane is a small holding area for what a viewer is looking at
// right now. The background lane carries the prefill passes, which may be
// thousands of calls deep; a full background queue must never reject a click.
const interactiveQueueLimit = 256

// nightStart and nightEnd are hours in Asia/Shanghai. Outside the window the
// background lane leaves most workers idle so an interactive request still gets
// a free slot.
const (
	nightStart                   = 0
	nightEnd                     = 8
	daytimeBackgroundConcurrency = 2
)

// Queue is an application-wide bounded scheduler with two lanes. Waiting
// requests hold no DB connection; duplicate readers share a job, and only their
// last cancellation stops it. Trials use the interactive lane but keep immediate
// busy rejection.
type Queue struct {
	mu                 sync.Mutex
	lifetime           context.Context
	run                func(func()) bool
	limits             Limits
	jobs               map[string]*queuedCall
	pendingInteractive []*queuedCall
	pendingBackground  []*queuedCall
	active             int
	backgroundActive   int
	readers            int
	// Now is the clock used for the day/night window; tests replace it.
	Now func() time.Time
}

type queuedCall struct {
	key        string
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	fn         func(context.Context) (Result, error)
	result     Result
	err        error
	readers    int
	running    bool
	background bool
}

type QueueStats struct {
	Active     int `json:"active"`
	Queued     int `json:"queued"`
	Background int `json:"queuedBackground"`
	Readers    int `json:"readers"`
}

func NewQueue(lifetime context.Context, limits Limits, run func(func()) bool) *Queue {
	return &Queue{lifetime: lifetime, limits: limits, run: run, jobs: map[string]*queuedCall{}, Now: time.Now}
}

func (q *Queue) Stats() QueueStats {
	q.mu.Lock()
	defer q.mu.Unlock()
	return QueueStats{q.active, len(q.pendingInteractive), len(q.pendingBackground), q.readers}
}

func (q *Queue) Configure(limits Limits) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.limits = limits
	for len(q.pendingInteractive) > interactiveQueueLimit {
		call := q.pendingInteractive[len(q.pendingInteractive)-1]
		q.pendingInteractive = q.pendingInteractive[:len(q.pendingInteractive)-1]
		q.rejectLocked(call, "translation_queue_full")
	}
	for len(q.pendingBackground) > limits.QueueLimit {
		call := q.pendingBackground[len(q.pendingBackground)-1]
		q.pendingBackground = q.pendingBackground[:len(q.pendingBackground)-1]
		q.rejectLocked(call, "translation_queue_full")
	}
	q.start()
}

func (q *Queue) rejectLocked(call *queuedCall, code string) {
	delete(q.jobs, call.key)
	call.err = failure(code, "The translation queue is full. Retry when it has room.")
	call.cancel()
	close(call.done)
}

// Do schedules interactive work. It keeps the previous signature so existing
// callers do not have to know about the lanes.
func (q *Queue) Do(ctx context.Context, key string, timeout time.Duration, trial bool, fn func(context.Context) (Result, error)) (Result, error) {
	return q.enqueue(ctx, key, timeout, trial, false, fn)
}

// DoBackground schedules prefill work. Background calls are only started while
// no interactive call waits, and their concurrency is lower in the daytime.
func (q *Queue) DoBackground(ctx context.Context, key string, timeout time.Duration, fn func(context.Context) (Result, error)) (Result, error) {
	return q.enqueue(ctx, key, timeout, false, true, fn)
}

func (q *Queue) enqueue(ctx context.Context, key string, timeout time.Duration, trial, background bool, fn func(context.Context) (Result, error)) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	q.mu.Lock()
	if ctx.Err() != nil || q.lifetime.Err() != nil {
		q.mu.Unlock()
		return Result{}, failure("translation_cancelled", "The translation request was cancelled.")
	}
	if q.readers >= 512 {
		q.mu.Unlock()
		return Result{}, failure("translation_queue_full", "The translation queue is full. Retry when it has room.")
	}
	call := q.jobs[key]
	if trial {
		call = nil
	}
	if call != nil && !background && call.background && !call.running {
		q.promoteLocked(call)
	}
	if call == nil {
		if trial && (q.active >= q.limits.Concurrency || len(q.pendingInteractive) > 0) {
			q.mu.Unlock()
			return Result{}, failure("translation_busy", "A translation is already running.")
		}
		if background {
			if len(q.pendingBackground) >= q.limits.QueueLimit {
				q.mu.Unlock()
				return Result{}, failure("translation_queue_full", "The translation queue is full. Retry when it has room.")
			}
		} else if len(q.pendingInteractive) >= interactiveQueueLimit {
			q.mu.Unlock()
			return Result{}, failure("translation_queue_full", "The translation queue is full. Retry when it has room.")
		}
		jobCtx, jobCancel := context.WithTimeout(q.lifetime, timeout)
		call = &queuedCall{key: key, ctx: jobCtx, cancel: jobCancel, done: make(chan struct{}), fn: fn, background: background}
		q.jobs[key] = call
		if background {
			q.pendingBackground = append(q.pendingBackground, call)
		} else {
			q.pendingInteractive = append(q.pendingInteractive, call)
		}
	}
	call.readers++
	q.readers++
	q.start()
	q.mu.Unlock()
	defer q.release(call)
	select {
	case <-ctx.Done():
		return Result{}, requestFailure(ctx, ctx.Err())
	case <-q.lifetime.Done():
		return Result{}, failure("translation_cancelled", "The translation request was cancelled.")
	case <-call.done:
		return call.result, call.err
	}
}

// promoteLocked moves a queued background job to the interactive lane so a
// viewer never waits behind a prefill pass for the same content.
func (q *Queue) promoteLocked(call *queuedCall) {
	for i, pending := range q.pendingBackground {
		if pending == call {
			q.pendingBackground = append(q.pendingBackground[:i], q.pendingBackground[i+1:]...)
			break
		}
	}
	call.background = false
	q.pendingInteractive = append(q.pendingInteractive, call)
}

// IsNightWindow reports whether the Shanghai prefill window is open. It is
// shared by the queue and the operator view so both describe one rule.
func IsNightWindow(now time.Time) bool {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		shanghai = time.FixedZone("CST", 8*3600)
	}
	return now.In(shanghai).Hour() < nightEnd
}

// backgroundCapacity is the number of simultaneous background calls allowed
// now. The night window uses the configured concurrency; daytime leaves two
// slots so an interactive request starts immediately.
func (q *Queue) backgroundCapacity() int {
	if q.limits.Concurrency <= 0 {
		return 1
	}
	now := q.Now
	if now == nil {
		now = time.Now
	}
	capacity := q.limits.Concurrency
	if !IsNightWindow(now()) {
		capacity = daytimeBackgroundConcurrency
	}
	if capacity > q.limits.Concurrency {
		capacity = q.limits.Concurrency
	}
	return capacity
}
func (q *Queue) start() {
	for {
		if q.active >= q.limits.Concurrency {
			return
		}
		var call *queuedCall
		if len(q.pendingInteractive) > 0 {
			call = q.pendingInteractive[0]
			q.pendingInteractive = q.pendingInteractive[1:]
		} else if len(q.pendingBackground) > 0 && q.backgroundActive < q.backgroundCapacity() {
			call = q.pendingBackground[0]
			q.pendingBackground = q.pendingBackground[1:]
		} else {
			return
		}
		q.launchLocked(call)
	}
}

func (q *Queue) launchLocked(call *queuedCall) {
	if call.ctx.Err() != nil {
		delete(q.jobs, call.key)
		call.err = requestFailure(call.ctx, call.ctx.Err())
		close(call.done)
		call.cancel()
		return
	}
	call.running = true
	q.active++
	if call.background {
		q.backgroundActive++
	}
	background := call.background
	if !q.run(func() {
		result, err := call.fn(call.ctx)
		q.mu.Lock()
		defer q.mu.Unlock()
		call.result, call.err = result, err
		call.cancel()
		q.active--
		if background {
			q.backgroundActive--
		}
		if q.jobs[call.key] == call {
			delete(q.jobs, call.key)
		}
		close(call.done)
		q.start()
	}) {
		call.err = failure("translation_cancelled", "The translation request was cancelled.")
		call.cancel()
		q.active--
		if background {
			q.backgroundActive--
		}
		delete(q.jobs, call.key)
		close(call.done)
	}
}

func (q *Queue) release(call *queuedCall) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.readers--
	call.readers--
	if call.readers > 0 {
		return
	}
	call.cancel()
	if q.jobs[call.key] != call {
		return
	}
	delete(q.jobs, call.key)
	if call.running {
		return
	}
	for i, pending := range q.pendingInteractive {
		if pending == call {
			q.pendingInteractive = append(q.pendingInteractive[:i], q.pendingInteractive[i+1:]...)
			return
		}
	}
	for i, pending := range q.pendingBackground {
		if pending == call {
			q.pendingBackground = append(q.pendingBackground[:i], q.pendingBackground[i+1:]...)
			return
		}
	}
}
