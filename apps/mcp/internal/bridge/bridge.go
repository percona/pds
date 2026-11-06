// Package bridge connects an agent's stdio to the control plane's /mcp endpoint. It forwards every
// message in both directions, signs each request with the bearer token the login left, and holds
// no tool code: the control plane decides which tools exist, so what the agent sees is what it
// would see anywhere else.
package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/percona/pds/apps/mcp/internal/messages"
)

// Tokens hands out the access token for the control plane. The oauth package's Source is the one
// implementation outside tests.
type Tokens interface {
	// Token returns a valid access token.
	Token(ctx context.Context) (string, error)
	// Refresh answers a 401 for the rejected token and returns the one to use next.
	Refresh(ctx context.Context, rejected string) (string, error)
}

// Options is one run of the bridge.
type Options struct {
	// Endpoint is the control plane's /mcp address.
	Endpoint string
	Tokens   Tokens
	// Version is the pds-mcp version, for the log.
	Version string
	// HTTP is the client that reaches the control plane, from oauth.NewHTTPClient. It is the only
	// thing that sends a token, and it refuses every other origin.
	HTTP   *http.Client
	Stdin  io.ReadCloser
	Stdout io.WriteCloser
	Log    *slog.Logger
}

// jsonrpcServerError is the JSON-RPC code of an error the server made up for a request it could
// not carry out.
const jsonrpcServerError = -32000

type bridge struct {
	opts    Options
	down    sdk.Connection
	up      sdk.Connection
	version atomic.Pointer[string]
	client  atomic.Pointer[string]

	writeDown sync.Mutex
	// initialize holds the id of the initialize request in flight, so its answer can be read for
	// the protocol version the agent and the control plane agreed on.
	initialize atomic.Pointer[jsonrpc.ID]
	inFlight   sync.WaitGroup
}

// Run forwards messages until the agent closes its input or ctx ends. It returns nil for a closed
// input and an error when the connection to the control plane breaks.
func Run(ctx context.Context, opts Options) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	b := &bridge{opts: opts}

	client := *opts.HTTP
	client.Transport = &statuses{base: opts.HTTP.Transport, version: &b.version, client: &b.client}

	var err error
	b.up, err = (&sdk.StreamableClientTransport{
		Endpoint:   opts.Endpoint,
		HTTPClient: &client,
		// The control plane serves each request on its own, so there is no standing stream to open
		// and no session to resume.
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
		OAuthHandler:         &authorizer{tokens: opts.Tokens},
	}).Connect(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = b.up.Close() }()

	b.down, err = (&sdk.IOTransport{Reader: opts.Stdin, Writer: opts.Stdout}).Connect(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = b.down.Close() }()

	upstream := make(chan error, 1)
	go func() { upstream <- b.pumpUp(ctx) }()

	err = b.pumpDown(ctx)
	if err == nil {
		// The agent closed its input. Calls already sent get their answers first.
		b.inFlight.Wait()

		return nil
	}
	cancel()
	<-upstream

	return err
}

// pumpDown reads what the agent sends and forwards it.
func (b *bridge) pumpDown(ctx context.Context) error {
	for {
		msg, err := b.down.Read(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}

			return err
		}

		if req, ok := msg.(*jsonrpc.Request); ok && req.Method == "initialize" && req.IsCall() {
			b.noteInitialize(req)
		}

		b.inFlight.Add(1)
		go func() {
			defer b.inFlight.Done()
			b.forward(ctx, msg)
		}()
	}
}

// noteInitialize remembers who the agent is, to pass on, and which request will carry the
// negotiated version back.
func (b *bridge) noteInitialize(req *jsonrpc.Request) {
	id := req.ID
	b.initialize.Store(&id)

	var params struct {
		ClientInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"clientInfo"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.ClientInfo.Name == "" {
		return
	}
	name := headerSafe(params.ClientInfo.Name)
	if v := headerSafe(params.ClientInfo.Version); v != "" {
		name += "/" + v
	}
	b.client.Store(&name)
}

// headerSafe keeps an agent's self reported name to printable ASCII and a short length, because it
// is sent as a header and is not verified.
func headerSafe(s string) string {
	var out strings.Builder
	for _, r := range s {
		if r >= 0x20 && r < 0x7f {
			out.WriteRune(r)
		}
		if out.Len() >= 100 {
			break
		}
	}

	return strings.TrimSpace(out.String())
}

// forward sends one message to the control plane. A message that cannot be sent is answered to the
// agent, when it expected an answer, with words it can act on.
func (b *bridge) forward(ctx context.Context, msg jsonrpc.Message) {
	err := b.up.Write(withAttempts(ctx), msg)
	if err == nil {
		return
	}

	b.opts.Log.WarnContext(ctx, "a request could not be forwarded", "reason", describe(err))

	req, ok := msg.(*jsonrpc.Request)
	if !ok || !req.IsCall() {
		return
	}

	// An error the control plane answered itself is passed on as it came.
	answer := failureFor(req, describe(err))
	if wire := serverError(err); wire != nil {
		answer = &jsonrpc.Response{ID: req.ID, Error: wire}
	}
	if werr := b.writeToAgent(ctx, answer); werr != nil {
		b.opts.Log.WarnContext(ctx, "the answer could not be written to the agent")
	}
}

// failureFor is the answer to a request that never reached the control plane. A tool call gets a
// tool error, which a model reads and relays; anything else gets a protocol error.
func failureFor(req *jsonrpc.Request, message string) *jsonrpc.Response {
	if req.Method == "tools/call" {
		result, _ := json.Marshal(&sdk.CallToolResult{
			IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: message}},
		})

		return &jsonrpc.Response{ID: req.ID, Result: result}
	}

	return &jsonrpc.Response{ID: req.ID, Error: &jsonrpc.Error{Code: jsonrpcServerError, Message: message}}
}

// pumpUp reads what the control plane sends and passes it on.
func (b *bridge) pumpUp(ctx context.Context) error {
	for {
		msg, err := b.up.Read(ctx)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, io.EOF) {
				return nil
			}
			b.opts.Log.ErrorContext(ctx, "the connection to the control plane broke", "reason", messages.Unreachable)

			return err
		}

		if resp, ok := msg.(*jsonrpc.Response); ok {
			b.noteVersion(resp)
		}
		if err := b.writeToAgent(ctx, msg); err != nil {
			return err
		}
	}
}

// noteVersion reads the protocol version from the answer to initialize.
func (b *bridge) noteVersion(resp *jsonrpc.Response) {
	pending := b.initialize.Load()
	if pending == nil || *pending != resp.ID || resp.Error != nil {
		return
	}
	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(resp.Result, &result); err == nil && result.ProtocolVersion != "" {
		v := headerSafe(result.ProtocolVersion)
		b.version.Store(&v)
	}
}

func (b *bridge) writeToAgent(ctx context.Context, msg jsonrpc.Message) error {
	b.writeDown.Lock()
	defer b.writeDown.Unlock()

	return b.down.Write(ctx, msg)
}
