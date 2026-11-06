// Package testutil holds fakes shared by tests in more than one package.
package testutil

import (
	"io"
	"net/http"
	"strings"
	"sync"
)

// ProviderStub answers every outbound provider call with one status or error,
// so a test never reaches a live service. It records what was sent.
type ProviderStub struct {
	Status int
	Err    error

	mu    sync.Mutex
	calls []*http.Request
}

// AcceptingProvider answers 200 with empty lists, the reply of a valid key.
func AcceptingProvider() *ProviderStub {
	return &ProviderStub{Status: http.StatusOK}
}

// RoundTrip implements http.RoundTripper.
func (p *ProviderStub) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}

	p.mu.Lock()
	p.calls = append(p.calls, req)
	p.mu.Unlock()

	if p.Err != nil {
		return nil, p.Err
	}

	return &http.Response{
		StatusCode: p.Status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"data":[],"items":[],"result":[]}`)),
		Request:    req,
	}, nil
}

// Calls is how many requests the stub has answered.
func (p *ProviderStub) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return len(p.calls)
}
