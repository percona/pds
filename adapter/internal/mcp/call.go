package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/percona/pds/adapter/internal/auth"
)

const requestIDHeader = "X-Request-Id"

// call is one tool call's way to the API. It sends the request a client of the
// public API would send, through the router in this process, as the caller the
// registry was built for.
type call struct {
	registry *Registry
	caller   auth.Caller
	// canAsk is true when the client can put a question to the person.
	canAsk bool
	// answers and state are what the client sent back to a question this call
	// asked, on the retry that carries them.
	answers sdk.InputResponseMap
	state   string
}

// get reads a path and decodes the answer into out. Fields out does not name
// are dropped, so a tool only passes on what its catalog entry describes.
//
// The failure is a *toolError: the API's refusal as the agent should read it.
func (c *call) get(ctx context.Context, path string, query url.Values, out any) error {
	target := path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	return c.send(ctx, http.MethodGet, target, nil, "", out)
}

// post sends a change with a JSON body and decodes the answer into out, or
// drops it when out is nil. check says what to call to find out whether the
// change was made, for the failures that do not say.
func (c *call) post(ctx context.Context, path string, body any, check string, out any) error {
	return c.send(ctx, http.MethodPost, path, body, check, out)
}

// remove sends a delete. The API answers it with no content.
func (c *call) remove(ctx context.Context, path, check string) error {
	return c.send(ctx, http.MethodDelete, path, nil, check, nil)
}

// send serves one request through the router. A change carries a check, and is
// sent once: whatever stops its answer from arriving leaves it unknown whether
// it was made, and the failure says so and never offers it again.
func (c *call) send(ctx context.Context, method, target string, body any, check string, out any) error {
	requestID := newRequestID()
	change := check != ""

	ctx, cancel := context.WithTimeout(auth.WithCaller(ctx, c.caller), c.registry.timeout)
	defer cancel()
	// A session opened inside a request handler inherits that request's routing
	// state through the context, and chi would route this request into it
	// rather than start its own. A nil routing context makes chi start one.
	ctx = context.WithValue(ctx, chi.RouteCtxKey, (*chi.Context)(nil))

	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return refuse(msgInvalidInput)
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://control-plane.internal"+target, payload)
	if err != nil {
		return refuse(msgInvalidInput)
	}
	req.Header.Set(requestIDHeader, requestID)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	rec := &capture{header: http.Header{}}
	c.registry.api.ServeHTTP(rec, req)

	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		if change {
			return unknownOutcome(kindTimeout, msgChangeTimedOut, check, requestID)
		}

		return &toolError{Kind: kindTimeout, Message: msgTimedOut, Retryable: true, RequestID: requestID}
	case ctx.Err() != nil:
		if change {
			return unknownOutcome(kindUnavailable, msgChangeUnreachable, check, requestID)
		}

		return &toolError{Kind: kindUnavailable, Message: msgUnavailable, Retryable: true, RequestID: requestID}
	}
	if rec.status >= http.StatusBadRequest {
		var failure struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(rec.body.Bytes(), &failure)

		return answered(rec.status, failure.Message, requestID)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(rec.body.Bytes(), out); err != nil {
		if change {
			return unknownOutcome(kindUnavailable, msgChangeUnreadable, check, requestID)
		}

		return &toolError{Kind: kindUnavailable, Message: msgUnreadable, Retryable: true, RequestID: requestID}
	}

	return nil
}

// newRequestID names a request so a customer can quote it to support. It
// identifies a request, not who sent it.
func newRequestID() string {
	var buf [4]byte
	// A short read leaves the zero id, which is still a usable trace name.
	_, _ = rand.Read(buf[:])

	return "mcp-" + hex.EncodeToString(buf[:])
}

// capture is the response writer a call hands the router. The standard
// recorder lives in a package that registers test flags, which a binary
// should not carry.
type capture struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (c *capture) Header() http.Header { return c.header }

func (c *capture) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}

func (c *capture) Write(p []byte) (int, error) {
	c.WriteHeader(http.StatusOK)

	return c.body.Write(p)
}
