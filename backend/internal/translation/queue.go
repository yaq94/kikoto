package translation

import (
	"context"
	"sync"
	"time"
)

// Queue is an application-wide bounded FIFO. Waiting requests hold no DB
// connection; duplicate readers share a job, and only their last cancellation
// stops it. Trials use the same lanes but retain immediate busy rejection.
type Queue struct {
	mu       sync.Mutex
	lifetime context.Context
	run      func(func()) bool
	limits   Limits
	jobs     map[string]*queuedCall
	pending  []*queuedCall
	active   int
	readers  int
}

type queuedCall struct {
	key     string
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	fn      func(context.Context) (Result, error)
	result  Result
	err     error
	readers int
	running bool
}

type QueueStats struct {
	Active  int `json:"active"`
	Queued  int `json:"queued"`
	Readers int `json:"readers"`
}

func NewQueue(lifetime context.Context, limits Limits, run func(func()) bool) *Queue {
	return &Queue{lifetime: lifetime, limits: limits, run: run, jobs: map[string]*queuedCall{}}
}

func (q *Queue) Stats() QueueStats {
	q.mu.Lock()
	defer q.mu.Unlock()
	return QueueStats{q.active, len(q.pending), q.readers}
}

func (q *Queue) Configure(limits Limits) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.limits = limits
	for len(q.pending) > limits.QueueLimit {
		call := q.pending[len(q.pending)-1]
		q.pending = q.pending[:len(q.pending)-1]
		delete(q.jobs, call.key)
		call.err = failure("translation_queue_full", "The translation queue is full. Retry when it has room.")
		call.cancel()
		close(call.done)
	}
	q.start()
}

func (q *Queue) Do(ctx context.Context, key string, timeout time.Duration, trial bool, fn func(context.Context) (Result, error)) (Result, error) {
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
	if call == nil {
		if trial && (q.active >= q.limits.Concurrency || len(q.pending) > 0) {
			q.mu.Unlock()
			return Result{}, failure("translation_busy", "A translation is already running.")
		}
		if q.active >= q.limits.Concurrency && len(q.pending) >= q.limits.QueueLimit {
			q.mu.Unlock()
			return Result{}, failure("translation_queue_full", "The translation queue is full. Retry when it has room.")
		}
		jobCtx, jobCancel := context.WithTimeout(q.lifetime, timeout)
		call = &queuedCall{key: key, ctx: jobCtx, cancel: jobCancel, done: make(chan struct{}), fn: fn}
		q.jobs[key] = call
		q.pending = append(q.pending, call)
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

func (q *Queue) start() {
	for q.active < q.limits.Concurrency && len(q.pending) > 0 {
		call := q.pending[0]
		q.pending = q.pending[1:]
		if call.ctx.Err() != nil {
			delete(q.jobs, call.key)
			call.err = requestFailure(call.ctx, call.ctx.Err())
			close(call.done)
			call.cancel()
			continue
		}
		call.running = true
		q.active++
		if !q.run(func() {
			result, err := call.fn(call.ctx)
			q.mu.Lock()
			defer q.mu.Unlock()
			call.result, call.err = result, err
			call.cancel()
			q.active--
			if q.jobs[call.key] == call {
				delete(q.jobs, call.key)
			}
			close(call.done)
			q.start()
		}) {
			call.err = failure("translation_cancelled", "The translation request was cancelled.")
			call.cancel()
			q.active--
			delete(q.jobs, call.key)
			close(call.done)
		}
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
	for i, pending := range q.pending {
		if pending == call {
			q.pending = append(q.pending[:i], q.pending[i+1:]...)
			break
		}
	}
}
