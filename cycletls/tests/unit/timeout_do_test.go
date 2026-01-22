package unit

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cycletls "github.com/Danny-Dasilva/CycleTLS/cycletls"
)

func TestCycleTLSDoTimeoutHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("late headers"))
	}))
	defer server.Close()

	client := cycletls.Init()
	defer client.Close()

	resp, err := client.Do(server.URL, cycletls.Options{
		Timeout: 1, // seconds
	}, "GET")
	if err != nil {
		t.Fatalf("expected timeout response, got error: %v", err)
	}
	if resp.Status != http.StatusRequestTimeout {
		t.Fatalf("expected status 408, got %d", resp.Status)
	}
	if !strings.Contains(strings.ToLower(resp.Body), "timeout") {
		t.Fatalf("expected timeout message, got %q", resp.Body)
	}
}

func TestCycleTLSDoTimeoutBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			_, _ = w.Write([]byte("partial"))
			flusher.Flush()
		}
		time.Sleep(2 * time.Second)
		_, _ = w.Write([]byte("never read"))
	}))
	defer server.Close()

	client := cycletls.Init()
	defer client.Close()

	resp, err := client.Do(server.URL, cycletls.Options{
		Timeout: 1, // seconds
	}, "GET")
	if err != nil {
		t.Fatalf("expected timeout response, got error: %v", err)
	}
	if resp.Status != http.StatusRequestTimeout {
		t.Fatalf("expected status 408, got %d", resp.Status)
	}
	if !strings.Contains(strings.ToLower(resp.Body), "timeout") {
		t.Fatalf("expected timeout message, got %q", resp.Body)
	}
}
