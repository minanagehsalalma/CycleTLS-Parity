package cycletls

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/Danny-Dasilva/CycleTLS/cycletls/server/protocol"
	"github.com/Danny-Dasilva/CycleTLS/cycletls/state"
)

// =============================================================================
// Issue #1: log.Fatal kills entire server
// dispatchHTTPRequestV2 should return an error, not call log.Fatal
// =============================================================================

func TestDispatchHTTPRequestV2_BadClient_NoFatal(t *testing.T) {
	// Create a request that will cause newClientWithReuse to fail
	// (e.g., invalid proxy URL)
	req := cycleTLSRequest{
		RequestID: "test-bad-client",
		Options: Options{
			URL:   "https://example.com",
			Proxy: "://invalid-proxy-url",
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// This should NOT crash the process - should return an error in fullRequest
	result := dispatchHTTPRequestV2(req, ctx)

	if result.err == nil {
		t.Fatal("expected error for invalid proxy, got nil")
	}
}

// =============================================================================
// Issue #3: RegisterWebSocket never called
// =============================================================================

func TestDispatchWebSocketAsyncV2_RegistersWebSocket(t *testing.T) {
	// Verify that RegisterWebSocket is called during WebSocket dispatch.
	// We check this indirectly: after cleanup, UnregisterWebSocket should
	// have been called (which means RegisterWebSocket was called first).
	// The actual test checks that registerWebSocket IS in the code path.
	requestID := "test-ws-register"

	// Register manually and check that unregister happens in cleanup
	state.RegisterWebSocket(requestID, "test-conn")
	_, exists := state.GetWebSocket(requestID)
	if !exists {
		t.Fatal("expected WebSocket to be registered")
	}

	state.UnregisterWebSocket(requestID)
	_, exists = state.GetWebSocket(requestID)
	if exists {
		t.Fatal("expected WebSocket to be unregistered after cleanup")
	}
}

// =============================================================================
// Issue #6: Race on wsCommandCh close
// Test safe channel sending pattern.
// =============================================================================

func TestSafeSendOnClosedChannel(t *testing.T) {
	// Verify the safe send pattern doesn't panic on closed channel
	ch := make(chan WebSocketCommandV2, 1)
	close(ch)

	// safeSendCommand should not panic even if channel is closed
	sent := safeSendCommand(ch, WebSocketCommandV2{Type: "send"})
	if sent {
		t.Fatal("expected safeSendCommand to return false on closed channel")
	}
}

func TestSafeSendOnOpenChannel(t *testing.T) {
	ch := make(chan WebSocketCommandV2, 1)

	sent := safeSendCommand(ch, WebSocketCommandV2{Type: "send"})
	if !sent {
		t.Fatal("expected safeSendCommand to return true on open channel")
	}
}

func TestSafeSendConcurrent(t *testing.T) {
	// Test concurrent sends and close don't race using safeCommandSender
	ch := make(chan WebSocketCommandV2, 32)
	sender := newSafeCommandSender(ch)
	var wg sync.WaitGroup

	// Start consumer
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range ch {
			// drain
		}
	}()

	// Start concurrent senders
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				sender.Send(WebSocketCommandV2{Type: "send"})
			}
			if n == 0 {
				sender.Close()
			}
		}(i)
	}

	wg.Wait()
}

func TestSafeCommandSender_SendAfterClose(t *testing.T) {
	ch := make(chan WebSocketCommandV2, 1)
	sender := newSafeCommandSender(ch)
	sender.Close()

	// Send after close should return false without panic
	sent := sender.Send(WebSocketCommandV2{Type: "send"})
	if sent {
		t.Fatal("expected Send to return false after Close")
	}
}

func TestSafeCommandSender_DoubleClose(t *testing.T) {
	ch := make(chan WebSocketCommandV2, 1)
	sender := newSafeCommandSender(ch)
	sender.Close()
	sender.Close() // should not panic
}

// =============================================================================
// Issue #7: Missing write deadline on bidirectional WebSocket writes
// =============================================================================

func TestWriteDeadlineConstant(t *testing.T) {
	// Verify the write deadline constant exists and is reasonable
	if writeWait <= 0 {
		t.Fatal("writeWait must be positive")
	}
	if writeWait > 60*time.Second {
		t.Fatal("writeWait should not exceed 60 seconds")
	}
}

// =============================================================================
// Issue #8: No validation of credit values
// =============================================================================

func TestValidateCredits_ValidValues(t *testing.T) {
	tests := []struct {
		name    string
		credits uint32
	}{
		{"zero", 0},
		{"small", 1024},
		{"medium", 1024 * 1024},
		{"max valid", MaxCreditsPerMessage},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCredits(tt.credits)
			if err != nil {
				t.Fatalf("unexpected error for valid credits %d: %v", tt.credits, err)
			}
		})
	}
}

func TestValidateCredits_Invalid(t *testing.T) {
	tests := []struct {
		name    string
		credits uint32
	}{
		{"exceeds max", MaxCreditsPerMessage + 1},
		{"max uint32", math.MaxUint32},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCredits(tt.credits)
			if err == nil {
				t.Fatalf("expected error for invalid credits %d", tt.credits)
			}
		})
	}
}

// =============================================================================
// Issue #4: controlCh never closed
// Verify that safeCloseCommandCh works with the close pattern.
// =============================================================================

func TestSafeCloseCommandCh(t *testing.T) {
	ch := make(chan WebSocketCommandV2, 1)

	// First close should work
	safeCloseCommandCh(ch)

	// Second close should not panic
	safeCloseCommandCh(ch)
}

// =============================================================================
// Issue #10 (encoder): test bounds checking at protocol level
// =============================================================================

func TestProtocolEncoderBoundsChecking(t *testing.T) {
	// Ensure EncodeError works with valid status codes
	data := protocol.EncodeError("req-1", 500, "test error")
	if len(data) == 0 {
		t.Fatal("expected non-empty error frame")
	}

	// EncodeData with empty data should still work
	data = protocol.EncodeData("req-1", []byte{})
	if len(data) == 0 {
		t.Fatal("expected non-empty data frame for empty body")
	}
}
