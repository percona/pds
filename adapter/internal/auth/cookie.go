package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"regexp"
	"strings"
)

// SessionCookieName is the cookie the browser carries a session in. The name
// is unremarkable on purpose: it says nothing about what is inside.
const SessionCookieName = "percona_session"

// ClientMCP is the kind of any external agent: a remote MCP client the control
// plane authenticated.
const ClientMCP = "mcp"

// ClientChat is the kind of a change made by the console's Chat. The control
// plane sets it on the in-process caller, so no sign in declares it.
const ClientChat = "chat"

// maxAgent caps an agent's name. It is shown beside the actor, so it stays
// short, but a client such as "Visual Studio Code" has to fit.
const maxAgent = 64

var (
	// agentPattern allows letters, digits, spaces inside the name and the
	// punctuation a product name uses. It starts and ends on a non-space.
	agentPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9 ._+-]*[A-Za-z0-9._+-])?$`)
)

// ValidAgent reports whether an agent name is short and made only of letters,
// digits, inner spaces and the punctuation a product name uses.
func ValidAgent(name string) bool {
	return len(name) <= maxAgent && agentPattern.MatchString(name)
}

// Client says what kind of program signed in, so the activity log can tell an
// agent from a person at a keyboard. The zero value means unspecified, which
// is what a browser session is.
//
// Agent names the program inside the kind: the OAuth client's name for an
// external agent, such as "Cursor", or the provider for the Chat, such as
// "Anthropic".
type Client struct {
	Kind  string
	Agent string
}

// Identity is who a request is from. Subject is who acted, and the Team in
// Scope is whose state they may reach. Client is how they signed in, and is the
// zero value for a browser session.
type Identity struct {
	Subject string
	// Scope is who the caller is in an Organization. The zero value means the
	// sign in knows of none.
	Scope  Scope
	Client Client
	// Method is how the session signed in: "password" or "oidc". The
	// operator token has none.
	Method string
}

// SignSessionID returns the cookie value for a session id: the id, a dot, and
// an HMAC of the id.
//
// The id is already unguessable and the database row is the authority, so the
// signature does not make a session harder to steal. What it buys is refusing
// a cookie this deployment never minted without spending a database read on
// it.
func SignSessionID(key []byte, id string) string {
	return id + "." + signature(key, id)
}

// VerifySessionID returns the session id from a cookie value, and reports
// whether the signature is one this key produced.
func VerifySessionID(key []byte, value string) (string, bool) {
	if len(key) == 0 {
		return "", false
	}

	id, sig, found := strings.Cut(value, ".")
	if !found || id == "" || sig == "" {
		return "", false
	}

	if !hmac.Equal([]byte(sig), []byte(signature(key, id))) {
		return "", false
	}

	return id, true
}

func signature(key []byte, id string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id))

	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// teamKey and clientKey type the context values so nothing else can collide
// with them.
type (
	teamKey   struct{}
	clientKey struct{}
)

// WithIdentity returns a context carrying the authenticated identity. It sets
// the subject too, so everything already reading SubjectFromContext records a
// real actor rather than a blank one.
func WithIdentity(ctx context.Context, identity Identity) context.Context {
	ctx = WithSubject(ctx, identity.Subject)
	ctx = context.WithValue(ctx, clientKey{}, identity.Client)
	if identity.Scope.OrganizationID != 0 {
		ctx = context.WithValue(ctx, scopeKey{}, identity.Scope)
	}

	return WithTeam(ctx, identity.Scope.Team.ID)
}

// WithTeam returns a context whose state requests reach the Team's. An agent
// call has no session to name one, so it is stamped from the token's owner.
func WithTeam(ctx context.Context, team uint) context.Context {
	return context.WithValue(ctx, teamKey{}, team)
}

// ClientFromContext returns how the request's session signed in. It is the
// zero Client when the sign in did not say, and under the stub.
func ClientFromContext(ctx context.Context) Client {
	client, _ := ctx.Value(clientKey{}).(Client)

	return client
}

// TeamFromContext returns the Team whose state this request may reach. It
// answers false under the stub, which is what keeps a deployment with no login
// from reading as one Team's session.
func TeamFromContext(ctx context.Context) (uint, bool) {
	team, ok := ctx.Value(teamKey{}).(uint)
	if !ok || team == 0 {
		return 0, false
	}

	return team, true
}
