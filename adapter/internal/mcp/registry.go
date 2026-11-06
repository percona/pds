// Package mcp is the registry of tools that AI agents use to work with
// databases through the control plane: one set of definitions that the Chat
// calls in process and that /mcp serves to MCP clients.
//
// A tool builds the request a client of the public API would send and serves
// it through the adapter's own router, as the caller. Everything the API
// decides, such as scope, feature flags and customer copy, stays the API's
// decision. The names, inputs, results and descriptions are the tool
// catalog's, and a test compares the descriptions with it.
//
// Adding a tool means writing its spec and function with define, and listing
// it in newTools. A tool never reads a store or a service directly.
//
// The server instructions and the prompts are the text files in guidance. They
// follow the caller's tier like the tools, and the customer copy test reads
// them as a client receives them.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/log"
)

// defaultTimeout is how long one request to the API may take.
const defaultTimeout = 30 * time.Second

// Registry holds the tools and the router they call.
type Registry struct {
	api     http.Handler
	logger  *slog.Logger
	version string
	timeout time.Duration
	clock   Clock
	tools   []tool
	// guide is the instructions and prompts every server of the registry says.
	guide guidance
	// consoleURL is where a person opens the console, for the tools that send
	// them there. It may be empty.
	consoleURL string
}

// Option changes how a Registry is built.
type Option func(*Registry)

// WithTimeout sets how long one request to the API may take.
func WithTimeout(timeout time.Duration) Option {
	return func(r *Registry) { r.timeout = timeout }
}

// WithClock sets the time the wait tools use.
func WithClock(clock Clock) Option {
	return func(r *Registry) { r.clock = clock }
}

// WithConsoleURL sets the address of the console, which create_environment
// gives the user to finish connecting an environment.
func WithConsoleURL(address string) Option {
	return func(r *Registry) { r.consoleURL = strings.TrimRight(strings.TrimSpace(address), "/") }
}

// New returns a registry whose tools call api, the adapter's router. version is
// the control plane release the MCP clients are told they are talking to.
func New(api http.Handler, logger *slog.Logger, version string, opts ...Option) *Registry {
	r := &Registry{
		api:     api,
		logger:  logger,
		version: version,
		timeout: defaultTimeout,
		clock:   systemClock{},
		tools:   newTools(),
		guide:   loadGuidance(),
	}
	for _, opt := range opts {
		opt(r)
	}

	return r
}

// newTools is every tool of the registry. The SDK lists them by name, so the
// order here does not reach a client.
func newTools() []tool {
	var tools []tool
	tools = append(tools, databaseReads()...)
	tools = append(tools, observabilityReads()...)
	tools = append(tools, environmentReads()...)
	tools = append(tools, historyReads()...)
	tools = append(tools, alertReads()...)
	tools = append(tools, supportReads()...)
	tools = append(tools, waitTools()...)
	tools = append(tools, databaseChanges()...)
	tools = append(tools, supportChanges()...)
	tools = append(tools, environmentChanges()...)

	return tools
}

// Server returns an MCP server whose tools act as the caller. The caller is
// fixed for the life of the server, so a server is built for one caller and
// never shared. It is cheap to build.
//
// The server lists, and will run, only the tools the caller's access allows.
// A call to a tool above it is refused as not allowed before any request is
// made. Its instructions and prompts follow the same tier, so neither names a
// tool the caller cannot use.
func (r *Registry) Server(caller auth.Caller) *sdk.Server {
	access := accessOf(caller)
	server := sdk.NewServer(&sdk.Implementation{Name: "pds", Version: r.version}, &sdk.ServerOptions{
		Logger:       r.logger,
		Instructions: r.guide.instructionsFor(access),
	})
	server.AddReceivingMiddleware(recoverPanics(r.logger), r.refuseAbove(access))
	for _, t := range r.tools {
		if access.Allows(t.access) {
			t.add(server, r, caller)
		}
	}
	r.guide.addPrompts(server, access)

	return server
}

// accessOf is the tier a caller's tools are served at. Every change is
// recorded with the client that made it, so a caller that names none of the
// clients the control plane knows may read and nothing more.
func accessOf(caller auth.Caller) auth.Access {
	switch caller.Identity.Client.Kind {
	case auth.ClientChat, auth.ClientMCP:
		return caller.Access
	default:
		return auth.AccessRead
	}
}

// refuseAbove answers a call to a tool the caller's access does not include.
// Such a tool is not registered, so without this the SDK would answer as it
// does for a tool that does not exist, and the agent could not tell that the
// user had not allowed it.
func (r *Registry) refuseAbove(access auth.Access) sdk.Middleware {
	needed := make(map[string]auth.Access, len(r.tools))
	for _, t := range r.tools {
		needed[t.name] = t.access
	}

	return func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			if call, ok := req.(*sdk.CallToolRequest); ok && method == "tools/call" {
				if tier, known := needed[call.Params.Name]; known && !access.Allows(tier) {
					message := msgDeleteNotAllowed
					if !access.Allows(auth.AccessChange) {
						message = msgChangesNotAllowed
					}

					return failure(&toolError{Kind: kindNotAllowed, Message: message}), nil
				}
			}

			return next(ctx, method, req)
		}
	}
}

// Session is an in-process MCP client connected to a server that acts as one
// caller. The Chat uses it as it would use any MCP client.
type Session struct {
	*sdk.ClientSession
	server *sdk.ServerSession
}

// Connect starts an in-process session for the caller. Nothing leaves the
// process: the client and the server talk over an in-memory pipe. Close the
// session when the caller is done.
func (r *Registry) Connect(ctx context.Context, caller auth.Caller) (*Session, error) {
	clientEnd, serverEnd := sdk.NewInMemoryTransports()

	server, err := r.Server(caller).Connect(ctx, serverEnd, nil)
	if err != nil {
		return nil, fmt.Errorf("start the tool server: %w", err)
	}

	client, err := sdk.NewClient(&sdk.Implementation{Name: "pds-chat", Version: r.version}, nil).Connect(ctx, clientEnd, nil)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("connect to the tool server: %w", err), server.Close())
	}

	return &Session{ClientSession: client, server: server}, nil
}

// Close ends the session on both sides.
func (s *Session) Close() error {
	return errors.Join(s.ClientSession.Close(), s.server.Close())
}

// recoverPanics keeps a panic in a tool from ending the process, which the
// SDK does not prevent. The call is answered as any other unexpected failure.
func recoverPanics(logger *slog.Logger) sdk.Middleware {
	return func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (result sdk.Result, err error) {
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}

				logger.ErrorContext(ctx, "recovered from panic", "method", method, "panic", log.RedactText(fmt.Sprint(recovered)))
				if method == "tools/call" {
					result, err = failure(&toolError{Kind: kindUnavailable, Message: msgInternal, Retryable: true}), nil

					return
				}
				result, err = nil, errors.New("internal error")
			}()

			return next(ctx, method, req)
		}
	}
}
