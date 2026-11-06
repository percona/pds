package mcp_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/mcp"
)

// The two protocol eras a client may speak. The older one is asked by the SDK
// calling the client during the call, the newer one by an input required
// result, and the tools must behave the same on both.
var eras = map[string]string{"2026-07-28": "", "2025-11-25": "2025-11-25"}

// person is a user who answers the questions a client shows them.
type person struct {
	mu     sync.Mutex
	asked  []*sdk.ElicitParams
	answer func(*sdk.ElicitParams) (*sdk.ElicitResult, error)
}

func answering(action string, content map[string]any) *person {
	return &person{answer: func(*sdk.ElicitParams) (*sdk.ElicitResult, error) {
		return &sdk.ElicitResult{Action: action, Content: content}, nil
	}}
}

func (p *person) handle(_ context.Context, req *sdk.ElicitRequest) (*sdk.ElicitResult, error) {
	p.mu.Lock()
	p.asked = append(p.asked, req.Params)
	p.mu.Unlock()

	return p.answer(req.Params)
}

func (p *person) questions() []*sdk.ElicitParams {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]*sdk.ElicitParams(nil), p.asked...)
}

// connectWith connects a client with the options to a server that acts as a
// caller of the tier, in memory, in the era the version names.
func connectWith(t *testing.T, api http.Handler, access auth.Access, version string, options *sdk.ClientOptions, opts ...mcp.Option) *sdk.ClientSession {
	t.Helper()

	clientEnd, serverEnd := sdk.NewInMemoryTransports()
	server, err := mcp.New(api, discardLogger(), "test", opts...).Server(callerAt(access)).Connect(t.Context(), serverEnd, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })

	session, err := sdk.NewClient(&sdk.Implementation{Name: "agent", Version: "1"}, options).
		Connect(t.Context(), clientEnd, &sdk.ClientSessionOptions{ProtocolVersion: version})
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	return session
}

func connectAsking(t *testing.T, api http.Handler, access auth.Access, version string, who *person, opts ...mcp.Option) *sdk.ClientSession {
	t.Helper()

	return connectWith(t, api, access, version, &sdk.ClientOptions{ElicitationHandler: who.handle}, opts...)
}

func callOn(t *testing.T, session *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()

	res, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)

	return res
}

func deleteAPI() *fakeAPI {
	api := changeAPI()
	api.then(databasePath, ok(databaseFixture()), ok(databaseFixture()), reply{http.StatusNoContent, nil})

	return api
}

func methods(api *fakeAPI) []string {
	var out []string
	for _, request := range api.sent() {
		out = append(out, request.Method)
	}

	return out
}

func openRange() map[string]any {
	return map[string]any{
		"name": "orders-dev", "environment": "spoke-a", "profile": "dev",
		"access": "public", "sourceRanges": []any{"203.0.113.0/24", "0.0.0.0/0"},
	}
}

func TestADeleteAsksThePersonToTypeTheNameAndRunsOnlyOnTheExactName(t *testing.T) {
	for era, version := range eras {
		t.Run(era, func(t *testing.T) {
			api := deleteAPI()
			who := answering("accept", map[string]any{"name": "orders-dev"})
			session := connectAsking(t, api, auth.AccessChangeDelete, version, who)

			res := callOn(t, session, deleteTool, map[string]any{"id": orders})

			require.False(t, res.IsError, text(t, res))
			questions := who.questions()
			require.Len(t, questions, 1, "the person is asked once")
			assert.Contains(t, questions[0].Message, "orders-dev")
			assert.Contains(t, questions[0].Message, "cannot be undone")
			assert.Equal(t, []string{http.MethodGet, http.MethodGet, http.MethodDelete}, methods(api))
		})
	}
}

func TestADeleteIsRefusedWhenTheTypedNameIsNotTheStoredName(t *testing.T) {
	for typed, content := range map[string]map[string]any{
		"another name": {"name": "orders-prod"},
		"capitals":     {"name": "Orders-Dev"},
		"a tail":       {"name": "orders-dev "},
		"the id":       {"name": orders},
	} {
		t.Run(typed, func(t *testing.T) {
			for era, version := range eras {
				api := deleteAPI()
				session := connectAsking(t, api, auth.AccessChangeDelete, version, answering("accept", content))

				res := callOn(t, session, deleteTool, map[string]any{"id": orders})

				require.True(t, res.IsError, era)
				assert.Equal(t, "invalid_input", structured(t, res)["kind"], era)
				assert.NotContains(t, methods(api), http.MethodDelete, era)
			}
		})
	}
}

func TestADeleteTheClientDeclinedOrCancelledChangesNothingAndSaysSo(t *testing.T) {
	for _, action := range []string{"decline", "cancel"} {
		for era, version := range eras {
			t.Run(action+" "+era, func(t *testing.T) {
				api := deleteAPI()
				session := connectAsking(t, api, auth.AccessChangeDelete, version, answering(action, nil))

				res := callOn(t, session, deleteTool, map[string]any{"id": orders})

				require.True(t, res.IsError)
				body := structured(t, res)
				assert.Equal(t, "confirmation_required", body["kind"])
				assert.Equal(t, false, body["retryable"])
				assert.Contains(t, body["message"], "nothing was changed")
				assert.NotContains(t, methods(api), http.MethodDelete)
			})
		}
	}
}

func TestADeleteAClientWithElicitationIsAskedEvenWhenTheModelPassedTheName(t *testing.T) {
	for era, version := range eras {
		t.Run(era, func(t *testing.T) {
			api := deleteAPI()
			who := answering("decline", nil)
			session := connectAsking(t, api, auth.AccessChangeDelete, version, who)

			res := callOn(t, session, deleteTool, map[string]any{"id": orders, "confirm": "orders-dev"})

			require.True(t, res.IsError, "the model's word is not the person's")
			assert.Len(t, who.questions(), 1)
			assert.NotContains(t, methods(api), http.MethodDelete)
		})
	}
}

func TestADeleteWhoseElicitationFailsChangesNothing(t *testing.T) {
	for era, version := range eras {
		t.Run(era, func(t *testing.T) {
			api := deleteAPI()
			who := &person{answer: func(*sdk.ElicitParams) (*sdk.ElicitResult, error) { return nil, errors.New("no window") }}
			session := connectAsking(t, api, auth.AccessChangeDelete, version, who)

			res, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: deleteTool, Arguments: map[string]any{"id": orders, "confirm": "orders-dev"}})

			assert.True(t, err != nil || res.IsError, "a failed question is never a yes")
			assert.NotContains(t, methods(api), http.MethodDelete)
		})
	}
}

func TestADeleteWithoutElicitationStillTakesTheConfirmArgument(t *testing.T) {
	for name, options := range map[string]*sdk.ClientOptions{
		"a client with no elicitation": nil,
		"a client with URL mode only":  {Capabilities: &sdk.ClientCapabilities{Elicitation: &sdk.ElicitationCapabilities{URL: &sdk.URLElicitationCapabilities{}}}},
	} {
		t.Run(name, func(t *testing.T) {
			api := deleteAPI()
			session := connectWith(t, api, auth.AccessChangeDelete, "", options)

			refused := callOn(t, session, deleteTool, map[string]any{"id": orders})
			require.True(t, refused.IsError)
			assert.Equal(t, "invalid_input", structured(t, refused)["kind"])
			assert.NotContains(t, methods(api), http.MethodDelete)

			done := callOn(t, session, deleteTool, map[string]any{"id": orders, "confirm": "orders-dev"})
			require.False(t, done.IsError, text(t, done))
			assert.Contains(t, methods(api), http.MethodDelete)
		})
	}
}

func TestADeleteAnsweredAfterTheConfirmationExpiredChangesNothing(t *testing.T) {
	for era, version := range eras {
		t.Run(era, func(t *testing.T) {
			api := deleteAPI()
			clock := newManualClock()
			who := &person{answer: func(*sdk.ElicitParams) (*sdk.ElicitResult, error) {
				clock.Advance(time.Hour)

				return &sdk.ElicitResult{Action: "accept", Content: map[string]any{"name": "orders-dev"}}, nil
			}}
			session := connectAsking(t, api, auth.AccessChangeDelete, version, who, mcp.WithClock(clock))

			res := callOn(t, session, deleteTool, map[string]any{"id": orders})

			require.True(t, res.IsError)
			body := structured(t, res)
			assert.Equal(t, "confirmation_required", body["kind"])
			assert.Contains(t, body["message"], "expired")
			assert.NotContains(t, methods(api), http.MethodDelete)
		})
	}
}

func TestAnOpenRangeAsksThePersonAndIsCreatedOnlyOnTheirYes(t *testing.T) {
	for era, version := range eras {
		t.Run(era, func(t *testing.T) {
			api := changeAPI()
			who := answering("accept", map[string]any{"confirm": true})
			session := connectAsking(t, api, auth.AccessChange, version, who)

			res := callOn(t, session, "create_database", openRange())

			require.False(t, res.IsError, text(t, res))
			questions := who.questions()
			require.Len(t, questions, 1, "the person is asked once")
			assert.Contains(t, questions[0].Message, "orders-dev")
			assert.Contains(t, questions[0].Message, "spoke-a")
			assert.Contains(t, questions[0].Message, "0.0.0.0/0")
			assert.Contains(t, questions[0].Message, "any address on the internet")
			assert.Equal(t, []any{"Any address on the internet can connect to this database."}, structured(t, res)["warnings"])
			require.Len(t, api.sent(), 1)
			assert.NotContains(t, api.sent()[0].Body, "acknowledge")
		})
	}
}

func TestAnOpenRangeTheClientDeclinedOrDidNotTickSendsNothing(t *testing.T) {
	answers := map[string]*person{
		"decline":    answering("decline", nil),
		"cancel":     answering("cancel", nil),
		"unticked":   answering("accept", map[string]any{"confirm": false}),
		"no content": answering("accept", nil),
	}
	for name, who := range answers {
		t.Run(name, func(t *testing.T) {
			for era, version := range eras {
				api := changeAPI()
				session := connectAsking(t, api, auth.AccessChange, version, who)

				res := callOn(t, session, "create_database", openRange())

				require.True(t, res.IsError, era)
				body := structured(t, res)
				assert.Equal(t, "confirmation_required", body["kind"], era)
				assert.Contains(t, body["message"], "nothing was created", era)
				assertNoRequest(t, api)
			}
		})
	}
}

func TestAnOpenRangeAClientWithElicitationIsAskedEvenWhenTheModelAcknowledged(t *testing.T) {
	api := changeAPI()
	who := answering("decline", nil)
	session := connectAsking(t, api, auth.AccessChange, "", who)
	args := openRange()
	args["acknowledgeOpenToInternet"] = true

	res := callOn(t, session, "create_database", args)

	require.True(t, res.IsError, "the model's word is not the person's")
	assert.Len(t, who.questions(), 1)
	assertNoRequest(t, api)
}

func TestAnOpenRangeWithoutElicitationStillTakesTheAcknowledgement(t *testing.T) {
	api := changeAPI()
	session := connectWith(t, api, auth.AccessChange, "", nil)

	refused := callOn(t, session, "create_database", openRange())
	require.True(t, refused.IsError)
	assert.Equal(t, "confirmation_required", structured(t, refused)["kind"])
	assertNoRequest(t, api)

	args := openRange()
	args["acknowledgeOpenToInternet"] = true
	done := callOn(t, session, "create_database", args)
	require.False(t, done.IsError, text(t, done))
	assert.Len(t, api.sent(), 1)
}

func TestARangeThatIsNotOpenAsksNobody(t *testing.T) {
	api := changeAPI()
	who := answering("decline", nil)
	session := connectAsking(t, api, auth.AccessChange, "", who)
	args := openRange()
	args["sourceRanges"] = []any{"203.0.113.0/24"}

	res := callOn(t, session, "create_database", args)

	require.False(t, res.IsError, text(t, res))
	assert.Empty(t, who.questions())
	assert.Len(t, api.sent(), 1)
}

func TestAnInvalidCreateIsRefusedBeforeAnyoneIsAsked(t *testing.T) {
	api := changeAPI()
	who := answering("accept", map[string]any{"confirm": true})
	session := connectAsking(t, api, auth.AccessChange, "", who)

	res := callOn(t, session, "create_database", map[string]any{
		"name": "orders-dev", "environment": "spoke-a", "profile": "dev", "access": "public",
	})

	require.True(t, res.IsError)
	assert.Empty(t, who.questions())
	assertNoRequest(t, api)
}

func TestAnAnswerIsBoundToTheCallItWasAskedFor(t *testing.T) {
	api := changeAPI()
	session := connectWith(t, api, auth.AccessChange, "", &sdk.ClientOptions{
		ElicitationHandler: answering("accept", map[string]any{"confirm": true}).handle,
		MultiRoundTrip:     &sdk.MultiRoundTripOptions{Disabled: true},
	})

	first, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: "create_database", Arguments: openRange()})
	require.NoError(t, err)
	require.True(t, first.NeedsInput(), "the call asks first")
	require.NotEmpty(t, first.RequestState)

	// The same answer and state, sent with another database.
	other := openRange()
	other["name"] = "payments-dev"
	retry, err := session.CallTool(t.Context(), &sdk.CallToolParams{
		Name: "create_database", Arguments: other, RequestState: first.RequestState,
		InputResponses: sdk.InputResponseMap{"confirm": &sdk.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}},
	})
	require.NoError(t, err)
	assert.True(t, retry.NeedsInput(), "an answer for one database is not an answer for another")
	assertNoRequest(t, api)

	// The right call with that answer is created.
	again, err := session.CallTool(t.Context(), &sdk.CallToolParams{
		Name: "create_database", Arguments: openRange(), RequestState: first.RequestState,
		InputResponses: sdk.InputResponseMap{"confirm": &sdk.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}},
	})
	require.NoError(t, err)
	assert.False(t, again.IsError || again.NeedsInput(), "the call the person answered goes through")
	assert.Len(t, api.sent(), 1)
}

func TestTheQuestionHoldsNoSecretAndNoMoreThanTheNameAndTheRanges(t *testing.T) {
	api := deleteAPI()
	who := answering("accept", map[string]any{"name": "orders-dev"})
	session := connectAsking(t, api, auth.AccessChangeDelete, "", who)

	callOn(t, session, deleteTool, map[string]any{"id": orders})

	questions := who.questions()
	require.Len(t, questions, 1)
	assert.NotContains(t, questions[0].Message, "mysql://")
	assert.NotContains(t, questions[0].Message, "orders-dev-secret")
	assert.Equal(t, "form", questions[0].Mode)
}
