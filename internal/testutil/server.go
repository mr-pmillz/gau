// Package testutil provides shared scaffolding for unit tests across the
// project. It is internal to the module so tests in any package can import it,
// but it is invisible to downstream consumers.
package testutil

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// QueueServer is an httptest.Server backed by a queue of handlers. Each
// incoming request consumes one handler from the queue (in order). When the
// queue is empty, the server fails the test and returns 500.
//
// This is the right shape for testing pagination loops: each provider page
// gets its own response scripted in advance.
type QueueServer struct {
	*httptest.Server
	mu       sync.Mutex
	handlers []http.HandlerFunc
	hits     atomic.Int64
}

// NewQueueServer returns a server that serves the given handlers in order.
// The server is registered with t.Cleanup so it shuts down when the test ends.
func NewQueueServer(t *testing.T, handlers ...http.HandlerFunc) *QueueServer {
	t.Helper()
	q := &QueueServer{handlers: handlers}
	q.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q.hits.Add(1)
		q.mu.Lock()
		if len(q.handlers) == 0 {
			q.mu.Unlock()
			t.Errorf("testutil.QueueServer: unexpected request to %s (queue empty)", r.URL.String())
			http.Error(w, "queue empty", http.StatusInternalServerError)
			return
		}
		h := q.handlers[0]
		q.handlers = q.handlers[1:]
		q.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(q.Close)
	return q
}

// Hits returns how many requests the server has handled. Useful for asserting
// that a rate-limited or paginated call hit the expected number of pages.
func (q *QueueServer) Hits() int64 {
	return q.hits.Load()
}

// Remaining returns the number of unconsumed handlers. A test that expects
// the server to be fully consumed can assert Remaining() == 0 in cleanup.
func (q *QueueServer) Remaining() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.handlers)
}

// JSON returns a handler that writes the given status code and body as
// application/json. Convenience wrapper for the common case.
func JSON(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// Status returns a handler that writes only a status code, no body.
func Status(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}
}
