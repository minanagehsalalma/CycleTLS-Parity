package state

import (
	"context"
	"log"
	"sync"
	"time"
)

const (
	// maxActiveRequests is the maximum number of tracked requests.
	// When exceeded, the oldest entries are evicted to prevent unbounded growth.
	maxActiveRequests = 10000
)

// requestEntry stores a cancel function alongside its registration time.
type requestEntry struct {
	cancel     context.CancelFunc
	registeredAt time.Time
}

// activeRequests maps request IDs to their entries
var activeRequests = make(map[string]requestEntry)

// activeRequestsMutex protects concurrent access to activeRequests
var activeRequestsMutex sync.Mutex

// RequestTrackerMaxSize returns the maximum number of tracked requests.
func RequestTrackerMaxSize() int {
	return maxActiveRequests
}

// RegisterRequest adds a request's cancel function to the tracker.
// This allows the request to be cancelled later by its ID.
// If the tracker is at capacity, stale entries are evicted first.
func RegisterRequest(id string, cancel context.CancelFunc) {
	activeRequestsMutex.Lock()
	defer activeRequestsMutex.Unlock()

	// Evict if at capacity
	if len(activeRequests) >= maxActiveRequests {
		evictOldestRequestLocked()
	}

	activeRequests[id] = requestEntry{
		cancel:       cancel,
		registeredAt: time.Now(),
	}
}

// UnregisterRequest removes a request from the tracker.
// This should be called when a request completes normally.
func UnregisterRequest(id string) {
	activeRequestsMutex.Lock()
	defer activeRequestsMutex.Unlock()
	delete(activeRequests, id)
}

// CancelRequest cancels and removes a request from the tracker.
// Returns true if the request was found and cancelled, false otherwise.
func CancelRequest(id string) bool {
	activeRequestsMutex.Lock()
	defer activeRequestsMutex.Unlock()
	if entry, exists := activeRequests[id]; exists {
		entry.cancel()
		delete(activeRequests, id)
		return true
	}
	return false
}

// CleanupStaleRequests removes entries older than maxAge and cancels them.
// Returns the number of entries removed.
func CleanupStaleRequests(maxAge time.Duration) int {
	activeRequestsMutex.Lock()
	defer activeRequestsMutex.Unlock()

	cutoff := time.Now().Add(-maxAge)
	removed := 0

	for id, entry := range activeRequests {
		if entry.registeredAt.Before(cutoff) {
			entry.cancel()
			delete(activeRequests, id)
			removed++
		}
	}

	if removed > 0 {
		log.Printf("state: cleaned up %d stale requests (older than %v)", removed, maxAge)
	}

	return removed
}

// recordRequestTime sets the registration time for a request (used in tests).
func recordRequestTime(id string, t time.Time) {
	activeRequestsMutex.Lock()
	defer activeRequestsMutex.Unlock()
	if entry, exists := activeRequests[id]; exists {
		entry.registeredAt = t
		activeRequests[id] = entry
	}
}

// evictOldestRequestLocked removes the oldest entry from the map.
// Caller must hold activeRequestsMutex.
func evictOldestRequestLocked() {
	var oldestID string
	var oldestTime time.Time
	first := true

	for id, entry := range activeRequests {
		if first || entry.registeredAt.Before(oldestTime) {
			oldestID = id
			oldestTime = entry.registeredAt
			first = false
		}
	}

	if !first {
		if entry, exists := activeRequests[oldestID]; exists {
			entry.cancel()
			delete(activeRequests, oldestID)
		}
	}
}
