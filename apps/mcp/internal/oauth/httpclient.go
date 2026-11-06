package oauth

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ErrOtherOrigin means a request was aimed somewhere other than the control plane.
var ErrOtherOrigin = errors.New("the request is not for the control plane")

// ErrRedirect means the control plane answered with a redirect, which is never followed.
var ErrRedirect = errors.New("the control plane answered with a redirect")

// maxBody caps what is read from the control plane in one response that is not a stream.
const maxBody = 1 << 20

// NewHTTPClient returns the only HTTP client that sends a token. It reaches the control plane's
// origin and nothing else, follows no redirect, and names the program in User-Agent. A request to
// another origin fails before anything is sent, so a bearer token cannot be aimed elsewhere by a
// metadata document, a redirect or a mistake. timeout is zero for a client that carries streams.
func NewHTTPClient(origin, userAgent string, timeout time.Duration) *http.Client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = http.ProxyFromEnvironment

	return &http.Client{
		Transport: &guard{origin: origin, userAgent: userAgent, base: base},
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return ErrRedirect
		},
	}
}

type guard struct {
	origin    string
	userAgent string
	base      http.RoundTripper
}

func (g *guard) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme+"://"+req.URL.Host != g.origin || req.URL.User != nil {
		return nil, ErrOtherOrigin
	}

	req = req.Clone(req.Context())
	req.Header.Set("User-Agent", g.userAgent)

	resp, err := g.base.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("reach the control plane: %w", err)
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 && resp.StatusCode != http.StatusNotModified {
		_ = resp.Body.Close()

		return nil, ErrRedirect
	}

	return resp, nil
}

// readBody reads a response body up to the cap.
func readBody(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("read the answer: %w", err)
	}
	if len(body) > maxBody {
		return nil, errors.New("the answer is too large")
	}

	return body, nil
}
