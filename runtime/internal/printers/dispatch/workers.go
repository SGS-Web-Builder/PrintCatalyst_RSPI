package dispatch

import (
	"context"
	"errors"
	"sync"
)

// Workers joins preparation/dispatch before the supervisor closes SQLite.
// Rendering is sequential and separate from the short physical-claim request.
type Workers struct {
	d       *Dispatcher
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	failure error
}

func NewWorkers(d *Dispatcher) *Workers { return &Workers{d: d} }
func (w *Workers) Name() string         { return "print-workers" }
func (w *Workers) Start(context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cancel != nil {
		return nil
	}
	if w.d == nil || w.d.docs == nil || w.d.backend == nil || w.d.config.Prepared == nil || w.d.config.Renderer == nil {
		return errors.New("print workers require backend, storage and renderer")
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.failure = nil
	w.done = make(chan struct{})
	done := w.done
	go func() {
		var wg sync.WaitGroup
		wg.Add(3)
		go func() { defer wg.Done(); w.recordExit(ctx, cancel, w.d.RunPreparation(ctx)) }()
		go func() { defer wg.Done(); w.recordExit(ctx, cancel, w.d.Run(ctx)) }()
		go func() { defer wg.Done(); w.d.runPreparedCleanup(ctx) }()
		wg.Wait()
		close(done)
	}()
	return nil
}
func (w *Workers) Ready(context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failure != nil {
		return w.failure
	}
	if w.cancel == nil {
		return errors.New("print workers stopped")
	}
	select {
	case <-w.done:
		return errors.New("print workers exited")
	default:
		return nil
	}
}
func (w *Workers) Stop(ctx context.Context) error {
	w.mu.Lock()
	cancel, done := w.cancel, w.done
	w.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	w.mu.Lock()
	w.cancel = nil
	w.done = nil
	w.mu.Unlock()
	return nil
}

// A dead essential worker must not leave the service reporting ready.
func (w *Workers) recordExit(ctx context.Context, cancel context.CancelFunc, err error) {
	if ctx.Err() != nil {
		return
	}
	if err == nil {
		err = errors.New("print worker stopped unexpectedly")
	}
	w.mu.Lock()
	w.failure = err
	w.mu.Unlock()
	cancel()
}
