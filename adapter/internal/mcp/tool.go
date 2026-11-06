package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/log"
)

// A tool is one entry of the registry. add puts it on a server, acting as the
// caller.
type tool struct {
	name string
	// access is the lowest tier that may use it.
	access auth.Access
	add    func(server *sdk.Server, registry *Registry, caller auth.Caller)
}

// spec is what the catalog fixes about a tool.
type spec struct {
	name        string
	title       string
	description string
	annotations *sdk.ToolAnnotations
	// access is the lowest tier that may use the tool. The zero value is
	// reading, so a tool has to ask for more.
	access auth.Access
	// tune sets what the Go types cannot say: enums, bounds and patterns.
	tune func(input, output *jsonschema.Schema)
}

// readOnly marks a tool that changes nothing and always answers the same for
// the same state. The hints are for clients to show, and nothing here relies
// on them.
func readOnly() *sdk.ToolAnnotations {
	closed := false

	return &sdk.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, DestructiveHint: &closed, OpenWorldHint: &closed}
}

// changing marks a tool that changes something. The hints are for clients to
// show, and nothing here relies on them. No tool reaches outside the control
// plane, so none is open world.
func changing(idempotent, destructive bool) *sdk.ToolAnnotations {
	closed := false

	return &sdk.ToolAnnotations{IdempotentHint: idempotent, DestructiveHint: &destructive, OpenWorldHint: &closed}
}

// define builds a tool from its spec and the function that runs it. In is the
// arguments, Out the result. The arguments are checked against In's schema
// before the function runs, and the result is sent as structured content and
// as one text block.
//
// It registers on the SDK's untyped AddTool, because the typed one replaces an
// error result's structured content with an empty result.
func define[In, Out any](s spec, run func(ctx context.Context, c *call, in In) (Out, error)) tool {
	input, output := schemaOf[In](), schemaOf[Out]()
	if s.tune != nil {
		s.tune(input, output)
	}
	// A model that sends a field the tool does not name is not worth an error.
	input.AdditionalProperties = nil
	validate := resolve(input)

	template := sdk.Tool{
		Name:         s.name,
		Title:        s.title,
		Description:  s.description,
		Annotations:  s.annotations,
		InputSchema:  input,
		OutputSchema: output,
	}

	add := func(server *sdk.Server, registry *Registry, caller auth.Caller) {
		registered := template
		server.AddTool(&registered, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			var in In
			if err := decodeArguments(req.Params.Arguments, validate, &in); err != nil {
				refused, _ := asToolError(err)

				return failure(refused), nil
			}

			out, err := run(ctx, &call{
				registry: registry, caller: caller,
				canAsk: canAsk(req.ClientCapabilities()), answers: req.Params.InputResponses, state: req.Params.RequestState,
			}, in)
			var asked *question
			if errors.As(err, &asked) {
				return &sdk.CallToolResult{
					InputRequests: sdk.InputRequestMap{confirmKey: asked.params}, RequestState: asked.state,
				}, nil
			}
			if err != nil {
				failed, expected := asToolError(err)
				if !expected {
					registry.logger.ErrorContext(ctx, "tool failed", "tool", s.name, "error", log.RedactText(err.Error()))
				}

				return failure(failed), nil
			}

			return success(ctx, registry, out), nil
		})
	}

	return tool{name: s.name, access: s.access, add: add}
}

var invalidPath = regexp.MustCompile(`/properties/([A-Za-z0-9_]+)`)

// decodeArguments checks the arguments against the tool's schema and decodes
// them. The schema library's own text can repeat what the model sent, so the
// refusal says which argument was wrong and nothing else.
func decodeArguments(raw json.RawMessage, schema *jsonschema.Resolved, in any) error {
	arguments := map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &arguments); err != nil {
			return refuse(msgInvalidArguments)
		}
	}

	if err := schema.Validate(arguments); err != nil {
		failure := refuse(msgInvalidArguments)
		if match := invalidPath.FindStringSubmatch(err.Error()); match != nil {
			failure.Hint = fmt.Sprintf("Check %s.", match[1])
		}

		return failure
	}

	body, err := json.Marshal(arguments)
	if err != nil {
		return refuse(msgInvalidArguments)
	}
	if err := json.Unmarshal(body, in); err != nil {
		return refuse(msgInvalidArguments)
	}

	return nil
}

// success is the result of a call that worked: the compact JSON twice, as
// structured content and as the one text block older clients read.
func success(ctx context.Context, registry *Registry, out any) *sdk.CallToolResult {
	body, err := json.Marshal(out)
	if err != nil {
		registry.logger.ErrorContext(ctx, "tool result did not marshal", "error", log.RedactText(err.Error()))

		return failure(&toolError{Kind: kindUnavailable, Message: msgInternal, Retryable: true})
	}

	return &sdk.CallToolResult{
		Content:           []sdk.Content{&sdk.TextContent{Text: string(body)}},
		StructuredContent: json.RawMessage(body),
	}
}

// failure is the same shape with isError set.
func failure(err *toolError) *sdk.CallToolResult {
	// A toolError holds only strings and a bool, so it always marshals.
	body, _ := json.Marshal(err)

	return &sdk.CallToolResult{
		IsError:           true,
		Content:           []sdk.Content{&sdk.TextContent{Text: string(body)}},
		StructuredContent: json.RawMessage(body),
	}
}
