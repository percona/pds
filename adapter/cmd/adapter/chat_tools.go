package main

import (
	"net/http"
	"sync/atomic"
)

// lateHandler is the router the Chat's tools call, handed over once it exists.
// The router serves the Chat, so the Chat is built first.
type lateHandler struct {
	target atomic.Pointer[http.Handler]
}

func (l *lateHandler) set(handler http.Handler) { l.target.Store(&handler) }

// ServeHTTP answers 503 until the router is set. A tool call cannot arrive
// earlier, since the Chat is only reached through the router.
func (l *lateHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	handler := l.target.Load()
	if handler == nil {
		http.Error(w, "starting", http.StatusServiceUnavailable)

		return
	}

	(*handler).ServeHTTP(w, r)
}
