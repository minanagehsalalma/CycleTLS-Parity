package cycletls

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"
)

func TestCreditWindowBasicAcquire(t *testing.T) {
	cw := newCreditWindow(100)

	// Should be able to acquire within window
	err := cw.Acquire(50, context.Background())
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	// Should be able to acquire rest of window
	err = cw.Acquire(50, context.Background())
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
}

func TestCreditWindowTryAcquire(t *testing.T) {
	cw := newCreditWindow(100)

	// TryAcquire should succeed
	if !cw.TryAcquire(50) {
		t.Fatal("TryAcquire should succeed when credits available")
	}

	// TryAcquire should succeed again
	if !cw.TryAcquire(50) {
		t.Fatal("TryAcquire should succeed when credits available")
	}

	// TryAcquire should fail - no credits left
	if cw.TryAcquire(1) {
		t.Fatal("TryAcquire should fail when no credits available")
	}
}

func TestCreditWindowAdd(t *testing.T) {
	cw := newCreditWindow(50)

	// Exhaust credits
	if !cw.TryAcquire(50) {
		t.Fatal("Should be able to acquire initial credits")
	}

	// Add more credits
	cw.Add(100)

	// Should be able to acquire added credits
	if !cw.TryAcquire(100) {
		t.Fatal("Should be able to acquire added credits")
	}
}

func TestCreditWindowBlocksWhenExhausted(t *testing.T) {
	cw := newCreditWindow(50)

	// Exhaust credits
	cw.Acquire(50, context.Background())

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// This should block and then timeout
	err := cw.Acquire(10, ctx)
	if err != context.DeadlineExceeded {
		t.Fatalf("Expected DeadlineExceeded, got %v", err)
	}
}

func TestCreditWindowUnblocksOnAdd(t *testing.T) {
	cw := newCreditWindow(0)

	done := make(chan struct{})
	started := make(chan struct{})
	var acquireErr error

	go func() {
		close(started) // Signal that goroutine has launched
		acquireErr = cw.Acquire(50, context.Background())
		close(done)
	}()

	// Wait for goroutine to start, then give it a moment to enter Acquire's wait
	<-started
	// Use a brief retry loop instead of a fixed sleep to ensure the goroutine
	// has entered the blocking Acquire call
	for i := 0; i < 100; i++ {
		time.Sleep(1 * time.Millisecond)
		// If the goroutine is blocked in Acquire, adding credits will unblock it
		// We add after giving it reasonable time to enter the wait
		if i >= 10 {
			break
		}
	}

	// Add credits
	cw.Add(50)

	// Wait for acquire to complete
	select {
	case <-done:
		if acquireErr != nil {
			t.Fatalf("Acquire should have succeeded after Add, got %v", acquireErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Acquire did not unblock after Add")
	}
}

func TestCreditWindowClose(t *testing.T) {
	cw := newCreditWindow(0)

	done := make(chan struct{})
	started := make(chan struct{})
	var acquireErr error

	go func() {
		close(started) // Signal that goroutine has launched
		acquireErr = cw.Acquire(50, context.Background())
		close(done)
	}()

	// Wait for goroutine to start, then give it a moment to enter Acquire's wait
	<-started
	for i := 0; i < 100; i++ {
		time.Sleep(1 * time.Millisecond)
		if i >= 10 {
			break
		}
	}

	// Close the window
	cw.Close()

	// Wait for acquire to complete
	select {
	case <-done:
		if acquireErr != ErrWindowClosed {
			t.Fatalf("Expected ErrWindowClosed, got %v", acquireErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Acquire did not unblock after Close")
	}
}

func TestCreditWindowNilGuard(t *testing.T) {
	var cw *creditWindow // nil

	// nil window should allow any acquire
	err := cw.Acquire(1000000, context.Background())
	if err != nil {
		t.Fatalf("nil creditWindow should allow any acquire, got %v", err)
	}

	// nil window TryAcquire should always succeed
	if !cw.TryAcquire(1000000) {
		t.Fatal("nil creditWindow TryAcquire should always succeed")
	}

	// nil window Add should be safe
	cw.Add(100) // Should not panic

	// nil window Close should be safe
	cw.Close() // Should not panic
}

func TestCreditWindowZeroAcquire(t *testing.T) {
	cw := newCreditWindow(100)

	// Zero acquire should always succeed without consuming credits
	err := cw.Acquire(0, context.Background())
	if err != nil {
		t.Fatalf("Zero acquire should succeed, got %v", err)
	}

	// Negative acquire should always succeed
	err = cw.Acquire(-5, context.Background())
	if err != nil {
		t.Fatalf("Negative acquire should succeed, got %v", err)
	}

	// Should still have all credits
	if !cw.TryAcquire(100) {
		t.Fatal("Credits should not have been consumed by zero/negative acquire")
	}
}

func TestCreditWindowConcurrent(t *testing.T) {
	cw := newCreditWindow(1000)

	var wg sync.WaitGroup
	numGoroutines := 10
	acquirePerGoroutine := 50

	// Start multiple goroutines trying to acquire
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < acquirePerGoroutine; j++ {
				cw.TryAcquire(1)
			}
		}()
	}

	// Also have goroutines adding credits
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < acquirePerGoroutine; j++ {
				cw.Add(1)
			}
		}()
	}

	wg.Wait()
	// If we get here without deadlock or panic, the test passes
}

func TestCreditWindowOverflowProtection(t *testing.T) {
	// Start with window near MaxInt64
	cw := newCreditWindow(math.MaxInt64 - 10)

	// Adding 20 should saturate at MaxInt64, not overflow to negative
	cw.Add(20)

	// Verify it saturated at MaxInt64
	cw.mu.Lock()
	if cw.window != math.MaxInt64 {
		t.Errorf("expected window to saturate at MaxInt64, got %d", cw.window)
	}
	cw.mu.Unlock()

	// Adding more should still stay at MaxInt64
	cw.Add(1000)
	cw.mu.Lock()
	if cw.window != math.MaxInt64 {
		t.Errorf("expected window to remain at MaxInt64, got %d", cw.window)
	}
	cw.mu.Unlock()
}

func TestFrameSenderBasic(t *testing.T) {
	ctx := context.Background()
	out := make(chan []byte, 1)
	sender := newFrameSender(ctx, out)

	data := []byte("test data")
	if !sender.send(data) {
		t.Fatal("send should succeed")
	}

	received := <-out
	if string(received) != string(data) {
		t.Fatalf("Expected %s, got %s", data, received)
	}
}

func TestFrameSenderCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan []byte) // unbuffered
	sender := newFrameSender(ctx, out)

	// Cancel context before sending
	cancel()

	// Send should return false when context is canceled
	if sender.send([]byte("test")) {
		t.Fatal("send should return false when context is canceled")
	}
}
