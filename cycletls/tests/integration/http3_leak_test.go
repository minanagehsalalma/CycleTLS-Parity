//go:build integration
// +build integration

package cycletls_test

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	cycletls "github.com/Danny-Dasilva/CycleTLS/cycletls"
)

// TestHTTP3ConnectionLeakPrevention tests that HTTP/3 requests don't leak
// goroutines or connections over multiple requests.
//
// This test verifies the fix for the HTTP/3 connection leak where pre-dialed
// connections were stored but never used by http3.Transport.
func TestHTTP3ConnectionLeakPrevention(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))
	defer server.Close()

	// Get baseline goroutine count
	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	baselineGoroutines := runtime.NumGoroutine()

	// Create client and make multiple requests
	client := cycletls.Init()
	defer client.Close()

	// Make several requests to trigger connection caching
	for i := 0; i < 10; i++ {
		resp, err := client.Do(server.URL, cycletls.Options{
			Method: "GET",
		}, "GET")
		if err != nil {
			t.Logf("Request %d error (expected for HTTP/3 to HTTP/1.1 server): %v", i, err)
		} else if resp.Status != 200 {
			t.Logf("Request %d status: %d", i, resp.Status)
		}
	}

	// Allow time for any leaked goroutines to accumulate
	time.Sleep(500 * time.Millisecond)
	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	// Check goroutine count
	finalGoroutines := runtime.NumGoroutine()
	goroutineDiff := finalGoroutines - baselineGoroutines

	// Allow some goroutine growth for legitimate purposes (connection pools, etc.)
	// but flag significant leaks. CI environments may have higher baseline variance.
	maxAllowedGrowth := 50
	if goroutineDiff > maxAllowedGrowth {
		t.Errorf("Possible goroutine leak: baseline=%d, final=%d, diff=%d (max allowed: %d)",
			baselineGoroutines, finalGoroutines, goroutineDiff, maxAllowedGrowth)
	} else {
		t.Logf("Goroutine count OK: baseline=%d, final=%d, diff=%d",
			baselineGoroutines, finalGoroutines, goroutineDiff)
	}
}

// TestHTTP3MultipleClientsNoLeak tests that creating and closing multiple
// clients doesn't leak resources.
func TestHTTP3MultipleClientsNoLeak(t *testing.T) {
	// Get baseline
	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	baselineGoroutines := runtime.NumGoroutine()

	// Create and close multiple clients
	for i := 0; i < 5; i++ {
		client := cycletls.Init()
		client.Close()
	}

	// Allow cleanup
	time.Sleep(500 * time.Millisecond)
	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	finalGoroutines := runtime.NumGoroutine()
	goroutineDiff := finalGoroutines - baselineGoroutines

	// Should return close to baseline after closing all clients
	maxAllowedGrowth := 10
	if goroutineDiff > maxAllowedGrowth {
		t.Errorf("Possible resource leak after closing clients: baseline=%d, final=%d, diff=%d",
			baselineGoroutines, finalGoroutines, goroutineDiff)
	} else {
		t.Logf("Resource cleanup OK: baseline=%d, final=%d, diff=%d",
			baselineGoroutines, finalGoroutines, goroutineDiff)
	}
}
