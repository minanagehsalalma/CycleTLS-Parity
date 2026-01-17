package cycletls

import (
	"context"
	"errors"
	"sync"
)

// ErrWindowClosed is returned when trying to acquire from a closed credit window.
var ErrWindowClosed = errors.New("credit window closed")

// -----------------------------------------------------------------------------
// Frame sender
// -----------------------------------------------------------------------------

// frameSender sends byte buffers through a channel with context cancellation support.
type frameSender struct {
	ctx context.Context
	out chan<- []byte
}

// newFrameSender creates a new frame sender.
func newFrameSender(ctx context.Context, out chan<- []byte) *frameSender {
	return &frameSender{
		ctx: ctx,
		out: out,
	}
}

// send transmits the buffer as-is.
// The caller must not modify buf after calling send.
func (s *frameSender) send(buf []byte) bool {
	select {
	case <-s.ctx.Done():
		return false
	case s.out <- buf:
		return true
	}
}

// -----------------------------------------------------------------------------
// Credit window (weighted semaphore)
// -----------------------------------------------------------------------------

// creditWindow is a weighted semaphore for flow control.
// A nil pointer represents infinite credit capacity.
type creditWindow struct {
	mu     sync.Mutex
	cond   *sync.Cond
	window int64
	closed bool
}

// newCreditWindow creates a new credit window with the specified initial capacity.
func newCreditWindow(initial int64) *creditWindow {
	if initial < 0 {
		panic("creditWindow: negative initial window")
	}

	cw := &creditWindow{
		window: initial,
	}
	cw.cond = sync.NewCond(&cw.mu)
	return cw
}

// guard checks if the request is trivial or infinite.
func (cw *creditWindow) guard(n int64) bool {
	return cw == nil || n <= 0
}

// Acquire blocks until n credits are available, then consumes them atomically.
// The wait is cancellable via the context.
func (cw *creditWindow) Acquire(n int64, ctx context.Context) error {
	if cw.guard(n) {
		return nil
	}

	cw.mu.Lock()
	defer cw.mu.Unlock()

	// Start a goroutine to watch for context cancellation
	// and wake up the condition when cancelled
	done := make(chan struct{})
	defer close(done)

	go func() {
		select {
		case <-ctx.Done():
			cw.cond.Broadcast()
		case <-done:
		}
	}()

	for {
		if cw.closed {
			return ErrWindowClosed
		}

		if cw.window >= n {
			cw.window -= n
			return nil
		}

		if err := ctx.Err(); err != nil {
			return err
		}

		cw.cond.Wait()
	}
}

// TryAcquire attempts to consume n credits without blocking.
func (cw *creditWindow) TryAcquire(n int64) bool {
	if cw.guard(n) {
		return true
	}

	cw.mu.Lock()
	defer cw.mu.Unlock()

	if cw.closed || cw.window < n {
		return false
	}

	cw.window -= n
	return true
}

// Add adds credits and wakes up waiting goroutines.
func (cw *creditWindow) Add(n int64) {
	if cw.guard(n) {
		return
	}

	cw.mu.Lock()
	defer cw.mu.Unlock()

	if cw.closed {
		return
	}

	cw.window += n
	cw.cond.Broadcast()
}

// Close logically closes the window and wakes all waiters.
func (cw *creditWindow) Close() {
	if cw == nil {
		return
	}

	cw.mu.Lock()
	defer cw.mu.Unlock()

	if cw.closed {
		return
	}

	cw.closed = true
	cw.cond.Broadcast()
}
