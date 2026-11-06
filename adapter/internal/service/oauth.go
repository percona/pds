package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/client/clientmeta"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/oauth"
	"github.com/percona/pds/adapter/internal/store"
)

// How long each piece of the authorization server lives.
const (
	// accessTokenLifetime is how long an access token works. A client holds a
	// refresh token to get the next one, so a stolen access token is short lived.
	accessTokenLifetime = time.Hour
	// authorizationCodeLifetime is how long a client has to exchange a code.
	authorizationCodeLifetime = time.Minute
	// pendingRequestLifetime is how long a person has to answer the consent screen.
	pendingRequestLifetime = 10 * time.Minute
	// unusedClientLifetime is how long a registration that never got a session is kept.
	unusedClientLifetime = 24 * time.Hour
	// agentTouchInterval is how stale a session's last use must be before a
	// request writes it again, so a busy agent is not a write per call.
	agentTouchInterval = time.Minute

	registrationLimit    = 10
	registrationWindow   = time.Hour
	registrationAllLimit = 300
	tokenLimit           = 60
	tokenWindow          = time.Minute
	authorizeLimit       = 30
	authorizeWindow      = time.Minute
	limiterKeys          = 10000
	// maxUnusedClients bounds registrations that never produced a session,
	// which anybody can make.
	maxUnusedClients = 10000

	maxRedirectURIs  = 10
	maxState         = 2048
	maxScopeLength   = 512
	unnamedAgent     = "Unnamed agent"
	clientKindCIMD   = "cimd"
	clientKindDCR    = "dcr"
	clientKindLocal  = "local"
	registeredPrefix = "dcr_"
)

// Errors a caller outside the token endpoints tells apart.
var (
	// ErrTokenInvalid means a bearer token is unknown, expired, revoked or its
	// session has ended. A caller answers 401 and says nothing more.
	ErrTokenInvalid = errors.New("that token is not valid")
	// ErrTokenAudience means a valid token was presented on a route it is not
	// bound to.
	ErrTokenAudience = errors.New("that token is not valid on this route")
	// ErrTokenScope means a valid token was presented on a route its grants do
	// not cover, such as the credential route without the passwords permission.
	ErrTokenScope = errors.New("that token does not grant this")
	// ErrAuthorizationGone means the parked authorization request is unknown,
	// expired, already decided, or belongs to another person.
	ErrAuthorizationGone = errors.New("that authorization request is no longer waiting")
	// ErrConsentGrants means the grants chosen exceed what the Organization
	// allows or what the client asked for.
	ErrConsentGrants = errors.New("those permissions are more than can be granted")
	// ErrConsentTarget means the Organization or Team chosen is not one the
	// person may give an agent.
	ErrConsentTarget = errors.New("the agent cannot act in that organization and team")
)

// OAuthStore is the authorization server's rows. The store's OAuth satisfies it.
type OAuthStore interface {
	CreateClient(ctx context.Context, client store.OAuthClient) error
	Client(ctx context.Context, id string) (store.OAuthClient, error)
	UnusedClients(ctx context.Context) (int64, error)
	MarkClientUsed(ctx context.Context, id string) error
	PurgeClients(ctx context.Context, cutoff time.Time) error
	CreateRequest(ctx context.Context, row store.OAuthRequest) error
	Request(ctx context.Context, hash string, now time.Time) (store.OAuthRequest, error)
	TakeRequest(ctx context.Context, hash string, now time.Time) (store.OAuthRequest, error)
	CreateCode(ctx context.Context, code store.OAuthCode) error
	UseCode(ctx context.Context, hash string, now time.Time) (store.OAuthCode, error)
	CreateAgentSession(ctx context.Context, session store.AgentSession, tokens []store.OAuthToken, codeHash string) error
	TokenSession(ctx context.Context, hash string) (store.OAuthToken, store.AgentSession, error)
	RotateRefresh(ctx context.Context, hash string, now time.Time, tokens []store.OAuthToken) error
	TouchAgentSession(ctx context.Context, id string, now time.Time) error
	AgentSessions(ctx context.Context, organizationID, userID uint, now time.Time) ([]store.AgentSession, error)
	DeleteAgentSession(ctx context.Context, id string) error
	PurgeOAuth(ctx context.Context, now time.Time) error
}

// OAuthPeople is who belongs to which Organization and Team. The Identity
// service satisfies it, so a deployment pinned to one Organization applies the
// same rule to an agent as to a browser.
type OAuthPeople interface {
	Membership(ctx context.Context, userID, organizationID uint) (store.Placement, error)
	Teams(ctx context.Context, organizationID uint) ([]auth.Team, error)
	Organizations(ctx context.Context, userID uint) ([]store.UserOrganization, error)
}

// ClientDocuments fetches a client's metadata document.
type ClientDocuments interface {
	Fetch(ctx context.Context, clientID string) (clientmeta.Document, error)
}

// OAuth is the control plane's OAuth authorization server for agents. It owns
// the rules; the HTTP layer only carries them.
type OAuth struct {
	store    OAuthStore
	people   OAuthPeople
	limits   AccessLimits
	docs     ClientDocuments
	members  OAuthMembers
	recorder ActivityRecorder
	issuer   string
	resource string
	now      func() time.Time
	lifetime time.Duration
	idle     time.Duration

	registrations *rateLimiter
	allRegistered *rateLimiter
	tokenRequests *rateLimiter
	authorizing   *rateLimiter
}

// OAuthOption configures NewOAuth.
type OAuthOption func(*OAuth)

// WithOAuthClock replaces the clock in tests.
func WithOAuthClock(now func() time.Time) OAuthOption { return func(o *OAuth) { o.now = now } }

// WithAgentSessionLifetime sets how long an agent session lasts however much
// it is used.
func WithAgentSessionLifetime(d time.Duration) OAuthOption {
	return func(o *OAuth) {
		if d > 0 {
			o.lifetime = d
		}
	}
}

// WithAgentSessionIdle sets how long an agent session survives unused.
func WithAgentSessionIdle(d time.Duration) OAuthOption {
	return func(o *OAuth) {
		if d > 0 {
			o.idle = d
		}
	}
}

// NewOAuth returns the authorization server. The issuer is this control
// plane's public address, with no trailing slash, and the one agents reach
// /mcp under.
func NewOAuth(
	rows OAuthStore, people OAuthPeople, limits AccessLimits, docs ClientDocuments, issuer string, opts ...OAuthOption,
) *OAuth {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	resource, _ := oauth.CanonicalResource(issuer + "/mcp")

	o := &OAuth{
		store: rows, people: people, limits: limits, docs: docs, issuer: issuer, resource: resource,
		now: time.Now, lifetime: defaultSessionLifetime, idle: defaultSessionIdle,
	}
	for _, opt := range opts {
		opt(o)
	}

	clock := func() time.Time { return o.now() }
	o.registrations = newRateLimiter(registrationLimit, registrationWindow, limiterKeys, clock)
	o.allRegistered = newRateLimiter(registrationAllLimit, registrationWindow, 1, clock)
	o.tokenRequests = newRateLimiter(tokenLimit, tokenWindow, limiterKeys, clock)
	o.authorizing = newRateLimiter(authorizeLimit, authorizeWindow, limiterKeys, clock)

	return o
}

// Issuer is the identifier clients see in the server metadata and in every
// authorization response.
func (o *OAuth) Issuer() string { return o.issuer }

// Resource is the canonical address of the MCP endpoint, the resource every
// token is for.
func (o *OAuth) Resource() string { return o.resource }

// OAuthError is a refusal at the token, registration or revocation endpoint,
// in the words of RFC 6749. Description is for the client's developer and the
// person reading its log, so it names no internals. Cause is for our log.
type OAuthError struct {
	Code        string
	Description string
	Cause       error
}

func (e *OAuthError) Error() string { return e.Code + ": " + e.Description }

func (e *OAuthError) Unwrap() error { return e.Cause }

func oauthErr(code, description string) *OAuthError {
	return &OAuthError{Code: code, Description: description}
}

func serverErr(cause error) *OAuthError {
	return &OAuthError{Code: "server_error", Description: "The request could not be completed. Try again.", Cause: cause}
}

func tooManyRequests() *OAuthError {
	return oauthErr("temporarily_unavailable", "Too many requests. Try again in a minute.")
}

// ClientRegistration is a Dynamic Client Registration request.
type ClientRegistration struct {
	ClientName              string
	RedirectURIs            []string
	TokenEndpointAuthMethod string
	GrantTypes              []string
	ResponseTypes           []string
}

// RegisteredClient is what registration answers.
type RegisteredClient struct {
	ClientID     string
	ClientName   string
	RedirectURIs []string
	IssuedAt     time.Time
}

// Register records a public client. from is the caller's address.
func (o *OAuth) Register(ctx context.Context, from string, in ClientRegistration) (RegisteredClient, *OAuthError) {
	if !o.registrations.allow(from) || !o.allRegistered.allow("") {
		return RegisteredClient{}, tooManyRequests()
	}

	if len(in.RedirectURIs) == 0 || len(in.RedirectURIs) > maxRedirectURIs {
		return RegisteredClient{}, oauthErr("invalid_redirect_uri", "Register between 1 and 10 redirect URIs.")
	}
	for _, uri := range in.RedirectURIs {
		if err := oauth.ValidateRedirectURI(uri); err != nil {
			return RegisteredClient{}, oauthErr("invalid_redirect_uri",
				"Redirect URIs must use https, or http on localhost, 127.0.0.1 or [::1].")
		}
	}
	if in.TokenEndpointAuthMethod != "" && in.TokenEndpointAuthMethod != "none" {
		return RegisteredClient{}, oauthErr("invalid_client_metadata", "Only public clients are supported: use the none authentication method.")
	}
	if !subset(in.GrantTypes, "authorization_code", "refresh_token") {
		return RegisteredClient{}, oauthErr("invalid_client_metadata", "Only the authorization_code and refresh_token grant types are supported.")
	}
	if !subset(in.ResponseTypes, "code") {
		return RegisteredClient{}, oauthErr("invalid_client_metadata", "Only the code response type is supported.")
	}

	name := oauth.DisplayName(in.ClientName)
	if name == "" {
		name = unnamedAgent
	}

	now := o.now()
	// Registration is open, so the table is kept from filling: registrations
	// that never produced a session are dropped after a day, and past a
	// ceiling new ones are refused.
	if err := o.store.PurgeClients(ctx, now.Add(-unusedClientLifetime)); err != nil {
		log.FromContext(ctx).WarnContext(ctx, "unused client registrations were not purged", "err", err)
	}
	unused, err := o.store.UnusedClients(ctx)
	if err != nil {
		return RegisteredClient{}, serverErr(err)
	}
	if unused >= maxUnusedClients {
		return RegisteredClient{}, tooManyRequests()
	}

	id, err := randomID(registeredPrefix, 16)
	if err != nil {
		return RegisteredClient{}, serverErr(err)
	}
	row := store.OAuthClient{ID: id, Name: name, RedirectURIs: slices.Clone(in.RedirectURIs), CreatedAt: now}
	if err := o.store.CreateClient(ctx, row); err != nil {
		return RegisteredClient{}, serverErr(err)
	}

	return RegisteredClient{ClientID: id, ClientName: name, RedirectURIs: row.RedirectURIs, IssuedAt: now}, nil
}

func subset(have []string, allowed ...string) bool {
	for _, h := range have {
		if !slices.Contains(allowed, h) {
			return false
		}
	}

	return true
}

func randomID(prefix string, size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}

	return prefix + hex.EncodeToString(raw), nil
}

// AuthorizationRequest is the query of GET /oauth/authorize.
type AuthorizationRequest struct {
	ClientID            string
	RedirectURI         string
	ResponseType        string
	CodeChallenge       string
	CodeChallengeMethod string
	State               string
	Scope               string
	Resource            string
}

// AuthorizationError is a refused authorization request. When RedirectURI is
// empty the request is not answered with a redirect, because nothing has
// proved the address is the client's: the person is shown an error page.
// When it is set it has been checked against the client's registration, and
// the error is returned there with State.
type AuthorizationError struct {
	Code        string
	Description string
	RedirectURI string
	State       string
}

func (e *AuthorizationError) Error() string { return e.Code + ": " + e.Description }

func pageError(code, description string) *AuthorizationError {
	return &AuthorizationError{Code: code, Description: description}
}

// CheckAuthorization checks what can be checked about a request without
// knowing the client: the shape of every parameter. It fetches nothing and
// never answers with a redirect, so it is what runs before the person has
// signed in.
func (o *OAuth) CheckAuthorization(req AuthorizationRequest) *AuthorizationError {
	if err := checkRequestShape(req); err != nil {
		return err
	}

	return o.checkParameters(req)
}

// checkRequestShape checks the parameters that name the client and the
// redirect address, which no redirect can report on.
func checkRequestShape(req AuthorizationRequest) *AuthorizationError {
	if req.ClientID == "" || len(req.ClientID) > 2048 {
		return pageError("invalid_request", "The request does not say which app is asking.")
	}
	if err := oauth.ValidateRedirectURI(req.RedirectURI); err != nil {
		return pageError("invalid_request", "The request has no valid redirect address.")
	}
	if len(req.State) > maxState || len(req.Scope) > maxScopeLength {
		return pageError("invalid_request", "The request is too long.")
	}

	return nil
}

// checkParameters is the part of the checks that is the same before and after
// the redirect is trusted.
func (o *OAuth) checkParameters(req AuthorizationRequest) *AuthorizationError {
	switch {
	case req.ResponseType != "code":
		return pageError("unsupported_response_type", "Only the code response type is supported.")
	case req.CodeChallengeMethod != "S256" || !oauth.ValidChallenge(req.CodeChallenge):
		return pageError("invalid_request", "A PKCE code challenge with the S256 method is required.")
	}
	if req.Resource != "" {
		if canonical, ok := oauth.CanonicalResource(req.Resource); !ok || canonical != o.resource {
			return pageError("invalid_target", "The resource is not this control plane's MCP endpoint.")
		}
	}
	if _, err := oauth.ParseScopes(req.Scope); err != nil {
		return pageError("invalid_scope", "A scope in the request is not offered here.")
	}

	return nil
}

// authorizingClient is a client resolved for one request.
type authorizingClient struct {
	name         string
	host         string
	kind         string
	redirectURIs []string
}

// StartAuthorization validates a request for a signed in person and parks it
// until they answer the consent screen. It returns the token that names the
// parked request, which only that person's browser should hold. The client's
// document is fetched here and not earlier, so nobody who has not signed in
// can make this server fetch an address.
func (o *OAuth) StartAuthorization(ctx context.Context, who auth.Identity, req AuthorizationRequest) (string, *AuthorizationError) {
	if who.Scope.UserID == 0 {
		return "", pageError("access_denied", "Sign in to continue.")
	}
	if !o.authorizing.allow(strconv.FormatUint(uint64(who.Scope.UserID), 10)) {
		return "", pageError("temporarily_unavailable", "Too many sign-in requests. Try again in a minute.")
	}

	if err := checkRequestShape(req); err != nil {
		return "", err
	}

	// Requests that were parked and never answered expire on their own; this
	// is what removes the rows, so the table cannot grow without a bound.
	if err := o.store.PurgeOAuth(ctx, o.now()); err != nil {
		log.FromContext(ctx).WarnContext(ctx, "expired authorization state was not purged", "err", err)
	}

	client, aerr := o.resolveClient(ctx, req.ClientID)
	if aerr != nil {
		return "", aerr
	}
	if !oauth.RedirectAllowed(client.redirectURIs, req.RedirectURI) {
		return "", pageError("invalid_request", "The redirect address does not belong to that app.")
	}

	// The redirect address belongs to the client now, so the rest are
	// reported to it.
	if err := o.checkParameters(req); err != nil {
		err.RedirectURI, err.State = req.RedirectURI, req.State

		return "", err
	}

	token, hash, err := auth.NewToken()
	if err != nil {
		return "", pageError("server_error", "The request could not be completed. Try again.")
	}

	now := o.now()
	resource := o.resource
	if err := o.store.CreateRequest(ctx, store.OAuthRequest{
		Hash: hash, UserID: who.Scope.UserID, ClientID: req.ClientID, ClientName: client.name, ClientHost: client.host,
		ClientKind: client.kind, RedirectURI: req.RedirectURI, Scope: req.Scope, State: req.State,
		CodeChallenge: req.CodeChallenge, Resource: resource, CreatedAt: now, ExpiresAt: now.Add(pendingRequestLifetime),
	}); err != nil {
		log.FromContext(ctx).ErrorContext(ctx, "authorization request could not be parked", "err", err)

		return "", pageError("server_error", "The request could not be completed. Try again.")
	}

	return token, nil
}

// resolveClient finds the client a request names: a document client by
// fetching its document, a registered one in the table.
func (o *OAuth) resolveClient(ctx context.Context, clientID string) (authorizingClient, *AuthorizationError) {
	switch {
	case clientID == oauth.LocalClientID:
		return authorizingClient{name: oauth.LocalClientName, kind: clientKindLocal, redirectURIs: oauth.LocalClientRedirectURIs()}, nil
	case strings.HasPrefix(clientID, "https://"):
		if o.docs == nil {
			return authorizingClient{}, pageError("invalid_client", "That app cannot be identified.")
		}
		doc, err := o.docs.Fetch(ctx, clientID)
		if err != nil {
			log.FromContext(ctx).WarnContext(ctx, "client metadata document was refused", "err", err)

			return authorizingClient{}, pageError("invalid_client", "That app cannot be identified.")
		}

		return authorizingClient{
			name: doc.ClientName, host: oauth.HostOf(clientID), kind: clientKindCIMD, redirectURIs: doc.RedirectURIs,
		}, nil
	case strings.HasPrefix(clientID, registeredPrefix):
		row, err := o.store.Client(ctx, clientID)
		if err != nil {
			if !errors.Is(err, store.ErrOAuthClientUnknown) {
				log.FromContext(ctx).ErrorContext(ctx, "client could not be read", "err", err)
			}

			return authorizingClient{}, pageError("invalid_client", "That app is not registered.")
		}

		return authorizingClient{name: row.Name, kind: clientKindDCR, redirectURIs: row.RedirectURIs}, nil
	default:
		return authorizingClient{}, pageError("invalid_client", "That app cannot be identified.")
	}
}

// PendingAuthorization is what the consent screen shows about a parked request.
type PendingAuthorization struct {
	// ClientName is what the client called itself. It is self asserted, so a
	// screen shows it as a label and not as proof of who is asking.
	ClientName string
	// ClientHost is the host of a document client's identifier, which the
	// client proved it controls. It is empty for a registered client.
	ClientHost string
	// ClientKind is "cimd" for a document client and "dcr" for a registered one.
	ClientKind       string
	RedirectURI      string
	RedirectHost     string
	LoopbackRedirect bool
	// Requested bounds what the person may grant.
	Requested oauth.Requested
	ExpiresAt time.Time
}

// Pending describes a parked request to the person who owns it.
func (o *OAuth) Pending(ctx context.Context, who auth.Identity, token string) (PendingAuthorization, error) {
	row, err := o.parked(ctx, who, token)
	if err != nil {
		return PendingAuthorization{}, err
	}

	requested, _ := oauth.ParseScopes(row.Scope)
	redirect, _ := url.Parse(row.RedirectURI)

	return PendingAuthorization{
		ClientName: row.ClientName, ClientHost: row.ClientHost, ClientKind: row.ClientKind,
		RedirectURI: row.RedirectURI, RedirectHost: redirect.Hostname(),
		LoopbackRedirect: oauth.IsLoopbackRedirect(row.RedirectURI),
		Requested:        requested, ExpiresAt: row.ExpiresAt,
	}, nil
}

// ConsentOrganization is one place the person may send the agent, with what
// they may give it there.
type ConsentOrganization struct {
	ID   uint
	Name string
	Role string
	// Teams are the Teams the person may pick: their own for a User, every
	// Team of the Organization for an admin.
	Teams     []auth.Team
	OwnTeamID uint
	// Access is the highest tier offered: the lowest of what the client asked
	// for, the Organization's limit and what the person's role may do.
	Access auth.Access
	// Passwords and SQLWrites are offered only to the local client, only when
	// the client asked, and only where the Organization allows them.
	Passwords bool
	SQLWrites bool
}

// ConsentScreen is everything the consent screen shows: who is asking and
// what the person may choose for each Organization they belong to.
type ConsentScreen struct {
	PendingAuthorization
	Organizations []ConsentOrganization
}

// ConsentScreen describes a parked request, and the choices, to the person
// who owns it.
func (o *OAuth) ConsentScreen(ctx context.Context, who auth.Identity, token string) (ConsentScreen, error) {
	pending, err := o.Pending(ctx, who, token)
	if err != nil {
		return ConsentScreen{}, err
	}

	memberships, err := o.people.Organizations(ctx, who.Scope.UserID)
	if err != nil {
		return ConsentScreen{}, fmt.Errorf("list organizations: %w", err)
	}

	screen := ConsentScreen{PendingAuthorization: pending, Organizations: make([]ConsentOrganization, 0, len(memberships))}
	local := pending.ClientKind == clientKindLocal
	for _, member := range memberships {
		placement, err := o.people.Membership(ctx, who.Scope.UserID, member.ID)
		if errors.Is(err, store.ErrNoMembership) {
			continue
		}
		if err != nil {
			return ConsentScreen{}, fmt.Errorf("read membership: %w", err)
		}
		settings, err := o.limits.AI(ctx, member.ID)
		if err != nil {
			return ConsentScreen{}, fmt.Errorf("read organization limits: %w", err)
		}
		teams, err := o.teamsFor(ctx, placement)
		if err != nil {
			return ConsentScreen{}, err
		}

		screen.Organizations = append(screen.Organizations, ConsentOrganization{
			ID: placement.OrganizationID, Name: placement.OrganizationName, Role: placement.Role,
			Teams: teams, OwnTeamID: placement.TeamID,
			Access:    auth.Lowest(pending.Requested.Access, CallerAccess(auth.Access(settings.Access), placement.Role)),
			Passwords: local && pending.Requested.Passwords && settings.Passwords,
			SQLWrites: local && pending.Requested.SQLWrites && settings.SQLWrites,
		})
	}

	return screen, nil
}

// teamsFor lists the Teams a person may act in: their own, or every Team of
// the Organization for an admin.
func (o *OAuth) teamsFor(ctx context.Context, placement store.Placement) ([]auth.Team, error) {
	if placement.Role != auth.RoleAdmin {
		return []auth.Team{{ID: placement.TeamID, Name: placement.TeamName}}, nil
	}

	teams, err := o.people.Teams(ctx, placement.OrganizationID)
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}

	return teams, nil
}

// parked reads a request and checks it is the caller's.
func (o *OAuth) parked(ctx context.Context, who auth.Identity, token string) (store.OAuthRequest, error) {
	if who.Scope.UserID == 0 || token == "" {
		return store.OAuthRequest{}, ErrAuthorizationGone
	}

	row, err := o.store.Request(ctx, auth.HashToken(token), o.now())
	if errors.Is(err, store.ErrOAuthRequestUnknown) || (err == nil && row.UserID != who.Scope.UserID) {
		return store.OAuthRequest{}, ErrAuthorizationGone
	}
	if err != nil {
		return store.OAuthRequest{}, err
	}

	return row, nil
}

// Consent is what the person chose on the consent screen: where the agent acts
// and what it may do.
type Consent struct {
	OrganizationID uint
	TeamID         uint
	Grants         oauth.Grants
}

// Approve ends a parked request with an authorization code and returns the
// address to send the browser to. The choice is checked against the person's
// membership, the Organization's limits and what the client asked for. A
// choice that is refused leaves the request waiting.
func (o *OAuth) Approve(ctx context.Context, who auth.Identity, token string, consent Consent) (string, error) {
	row, err := o.parked(ctx, who, token)
	if err != nil {
		return "", err
	}

	placement, err := o.people.Membership(ctx, who.Scope.UserID, consent.OrganizationID)
	if errors.Is(err, store.ErrNoMembership) {
		return "", ErrConsentTarget
	}
	if err != nil {
		return "", fmt.Errorf("read membership: %w", err)
	}
	if _, err := o.resolveTeam(ctx, placement, consent.TeamID); err != nil {
		return "", err
	}
	if err := o.checkGrants(ctx, placement, row, consent.Grants); err != nil {
		return "", err
	}

	// The check read the request; this is the step that spends it, so two
	// approvals of one request make one code.
	if _, err := o.store.TakeRequest(ctx, auth.HashToken(token), o.now()); err != nil {
		if errors.Is(err, store.ErrOAuthRequestUnknown) {
			return "", ErrAuthorizationGone
		}

		return "", err
	}

	code, hash, err := auth.NewToken()
	if err != nil {
		return "", err
	}
	now := o.now()
	if err := o.store.CreateCode(ctx, store.OAuthCode{
		Hash: hash, UserID: who.Scope.UserID, OrganizationID: consent.OrganizationID, TeamID: consent.TeamID,
		Method: who.Method, Access: string(consent.Grants.Access), Passwords: consent.Grants.Passwords,
		SQLWrites: consent.Grants.SQLWrites, ClientID: row.ClientID, ClientName: row.ClientName,
		ClientHost: row.ClientHost, ClientKind: row.ClientKind, RedirectURI: row.RedirectURI,
		CodeChallenge: row.CodeChallenge, Resource: row.Resource, CreatedAt: now, ExpiresAt: now.Add(authorizationCodeLifetime),
	}); err != nil {
		return "", err
	}

	log.FromContext(ctx).InfoContext(ctx, "an agent was signed in",
		"user", who.Scope.UserID, "organization", consent.OrganizationID, "team", consent.TeamID,
		"client_kind", row.ClientKind, "client_host", row.ClientHost, "access", consent.Grants.Access,
		"passwords", consent.Grants.Passwords, "sql_writes", consent.Grants.SQLWrites)

	return o.redirect(row.RedirectURI, map[string]string{"code": code, "state": row.State}), nil
}

// resolveTeam allows a User their own Team and an admin any Team of the
// Organization, and returns it.
func (o *OAuth) resolveTeam(ctx context.Context, placement store.Placement, team uint) (auth.Team, error) {
	teams, err := o.teamsFor(ctx, placement)
	if err != nil {
		return auth.Team{}, err
	}
	for _, t := range teams {
		if t.ID == team {
			return t, nil
		}
	}

	return auth.Team{}, ErrConsentTarget
}

// checkGrants refuses grants beyond what the client asked for, what the
// Organization allows, or what the person's role may do.
func (o *OAuth) checkGrants(ctx context.Context, placement store.Placement, row store.OAuthRequest, grants oauth.Grants) error {
	if !grants.Access.Valid() {
		return ErrConsentGrants
	}

	requested, err := oauth.ParseScopes(row.Scope)
	if err != nil || !requested.Allows(grants) {
		return ErrConsentGrants
	}
	// Passwords and SQL writes leave the control plane for a person's own
	// computer, so only the client that ships with it may hold them.
	if (grants.Passwords || grants.SQLWrites) && row.ClientKind != clientKindLocal {
		return ErrConsentGrants
	}

	settings, err := o.limits.AI(ctx, placement.OrganizationID)
	if err != nil {
		return fmt.Errorf("read organization limits: %w", err)
	}
	ceiling := CallerAccess(auth.Access(settings.Access), placement.Role)
	if !ceiling.Allows(grants.Access) || (grants.Passwords && !settings.Passwords) || (grants.SQLWrites && !settings.SQLWrites) {
		return ErrConsentGrants
	}

	return nil
}

// Deny ends a parked request with access_denied and returns the address to
// send the browser to.
func (o *OAuth) Deny(ctx context.Context, who auth.Identity, token string) (string, error) {
	row, err := o.parked(ctx, who, token)
	if err != nil {
		return "", err
	}
	if _, err := o.store.TakeRequest(ctx, auth.HashToken(token), o.now()); err != nil {
		if errors.Is(err, store.ErrOAuthRequestUnknown) {
			return "", ErrAuthorizationGone
		}

		return "", err
	}

	log.FromContext(ctx).InfoContext(ctx, "a person did not allow an agent to sign in",
		"user", who.Scope.UserID, "client_kind", row.ClientKind, "client_host", row.ClientHost)

	return o.redirect(row.RedirectURI, map[string]string{
		"error": "access_denied", "error_description": "The person did not allow access.", "state": row.State,
	}), nil
}

// ErrorRedirect is where a refused request is sent, for an error the client's
// own redirect address may receive.
func (o *OAuth) ErrorRedirect(err *AuthorizationError) string {
	return o.redirect(err.RedirectURI, map[string]string{
		"error": err.Code, "error_description": err.Description, "state": err.State,
	})
}

// redirect adds the parameters of an authorization response, and the issuer
// every response carries, to a redirect address the client registered.
func (o *OAuth) redirect(base string, params map[string]string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}

	query := u.Query()
	for name, value := range params {
		if value != "" {
			query.Set(name, value)
		}
	}
	query.Set("iss", o.issuer)
	u.RawQuery = query.Encode()

	return u.String()
}

// TokenRequest is an authorization_code grant at the token endpoint.
type TokenRequest struct {
	ClientID     string
	Code         string
	RedirectURI  string
	CodeVerifier string
	Resource     string
}

// RefreshRequest is a refresh_token grant.
type RefreshRequest struct {
	ClientID     string
	RefreshToken string
	Resource     string
	Scope        string
}

// RevokeRequest asks to end a session by one of its tokens.
type RevokeRequest struct {
	Token    string
	ClientID string
}

// TokenResponse is a successful token response.
type TokenResponse struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	ExpiresIn    int
	// Scope is the session's grants.
	Scope string
}

// ExchangeCode trades an authorization code for tokens. The code is spent
// before anything else is checked, so a stolen code cannot be tried against
// many verifiers, and a code presented twice ends the session the first use
// made.
func (o *OAuth) ExchangeCode(ctx context.Context, from string, req TokenRequest) (TokenResponse, *OAuthError) {
	if !o.tokenRequests.allow(from) {
		return TokenResponse{}, tooManyRequests()
	}
	if req.Code == "" || req.CodeVerifier == "" || req.RedirectURI == "" || req.ClientID == "" {
		return TokenResponse{}, oauthErr("invalid_request", "The code, redirect_uri, client_id and code_verifier parameters are required.")
	}

	now := o.now()
	code, err := o.store.UseCode(ctx, auth.HashToken(req.Code), now)
	switch {
	case errors.Is(err, store.ErrOAuthCodeUsed):
		if code.SessionID != "" {
			log.FromContext(ctx).WarnContext(ctx, "an authorization code was used twice, the session it made was ended")
			if err := o.store.DeleteAgentSession(ctx, code.SessionID); err != nil {
				return TokenResponse{}, serverErr(err)
			}
		}

		return TokenResponse{}, invalidGrant()
	case errors.Is(err, store.ErrOAuthCodeUnknown):
		return TokenResponse{}, invalidGrant()
	case err != nil:
		return TokenResponse{}, serverErr(err)
	}

	if code.ClientID != req.ClientID || code.RedirectURI != req.RedirectURI || !oauth.VerifyPKCE(code.CodeChallenge, req.CodeVerifier) {
		return TokenResponse{}, invalidGrant()
	}
	if err := o.checkResource(req.Resource); err != nil {
		return TokenResponse{}, err
	}

	session := store.AgentSession{
		UserID: code.UserID, OrganizationID: code.OrganizationID, TeamID: code.TeamID, Method: code.Method,
		Access: code.Access, Passwords: code.Passwords, SQLWrites: code.SQLWrites, ClientID: code.ClientID,
		ClientName: code.ClientName, ClientHost: code.ClientHost, ClientKind: code.ClientKind, Resource: code.Resource,
		CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(o.lifetime),
	}
	id, err := randomID("", 16)
	if err != nil {
		return TokenResponse{}, serverErr(err)
	}
	session.ID = id

	response, tokens, err := o.mint(session, now)
	if err != nil {
		return TokenResponse{}, serverErr(err)
	}
	if err := o.store.CreateAgentSession(ctx, session, tokens, code.Hash); err != nil {
		return TokenResponse{}, serverErr(err)
	}
	if strings.HasPrefix(code.ClientID, registeredPrefix) {
		if err := o.store.MarkClientUsed(ctx, code.ClientID); err != nil {
			log.FromContext(ctx).WarnContext(ctx, "client could not be marked used", "err", err)
		}
	}
	if err := o.store.PurgeOAuth(ctx, now); err != nil {
		log.FromContext(ctx).WarnContext(ctx, "expired authorization state was not purged", "err", err)
	}

	return response, nil
}

func invalidGrant() *OAuthError {
	return oauthErr("invalid_grant", "That grant is not valid. Sign in again.")
}

func (o *OAuth) checkResource(raw string) *OAuthError {
	if raw == "" {
		return nil
	}
	if canonical, ok := oauth.CanonicalResource(raw); !ok || canonical != o.resource {
		return oauthErr("invalid_target", "The resource is not this control plane's MCP endpoint.")
	}

	return nil
}

// mint makes the access and refresh tokens for a session. The access token
// never outlives the session.
func (o *OAuth) mint(session store.AgentSession, now time.Time) (TokenResponse, []store.OAuthToken, error) {
	access, accessHash, err := auth.NewToken()
	if err != nil {
		return TokenResponse{}, nil, err
	}
	refresh, refreshHash, err := auth.NewToken()
	if err != nil {
		return TokenResponse{}, nil, err
	}

	accessExpires := now.Add(accessTokenLifetime)
	if accessExpires.After(session.ExpiresAt) {
		accessExpires = session.ExpiresAt
	}

	grants := oauth.Grants{Access: auth.Access(session.Access), Passwords: session.Passwords, SQLWrites: session.SQLWrites}

	response := TokenResponse{
		AccessToken: access, RefreshToken: refresh, TokenType: "Bearer",
		ExpiresIn: int(accessExpires.Sub(now) / time.Second), Scope: strings.Join(grants.Scopes(), " "),
	}
	tokens := []store.OAuthToken{
		{Hash: accessHash, SessionID: session.ID, Kind: store.TokenAccess, ExpiresAt: accessExpires},
		{Hash: refreshHash, SessionID: session.ID, Kind: store.TokenRefresh, ExpiresAt: session.ExpiresAt},
	}

	return response, tokens, nil
}

// alive reports whether a session is inside both its limits.
func (o *OAuth) alive(session store.AgentSession, now time.Time) bool {
	return now.Before(session.ExpiresAt) && now.Before(session.LastUsedAt.Add(o.idle))
}

// Refresh trades a refresh token for new tokens. The refresh token is retired
// in the same step, and presenting a retired one ends the session: whoever
// holds the older token and whoever holds the newer one cannot both be the
// client.
func (o *OAuth) Refresh(ctx context.Context, from string, req RefreshRequest) (TokenResponse, *OAuthError) {
	if !o.tokenRequests.allow(from) {
		return TokenResponse{}, tooManyRequests()
	}
	if req.RefreshToken == "" || req.ClientID == "" {
		return TokenResponse{}, oauthErr("invalid_request", "The refresh_token and client_id parameters are required.")
	}
	if err := o.checkResource(req.Resource); err != nil {
		return TokenResponse{}, err
	}

	now := o.now()
	hash := auth.HashToken(req.RefreshToken)
	token, session, err := o.store.TokenSession(ctx, hash)
	if errors.Is(err, store.ErrOAuthTokenUnknown) {
		return TokenResponse{}, invalidGrant()
	}
	if err != nil {
		return TokenResponse{}, serverErr(err)
	}
	if token.Kind != store.TokenRefresh || session.ClientID != req.ClientID {
		return TokenResponse{}, invalidGrant()
	}

	grants := oauth.Grants{Access: auth.Access(session.Access), Passwords: session.Passwords, SQLWrites: session.SQLWrites}
	if req.Scope != "" {
		requested, err := oauth.ParseScopes(req.Scope)
		if err != nil || !within(requested, grants) {
			return TokenResponse{}, oauthErr("invalid_scope", "A scope in the request was not granted.")
		}
	}

	if token.RotatedAt != nil {
		return TokenResponse{}, o.reused(ctx, session.ID)
	}
	if !o.alive(session, now) || !now.Before(token.ExpiresAt) {
		return TokenResponse{}, o.endSession(ctx, session.ID)
	}
	if _, err := o.people.Membership(ctx, session.UserID, session.OrganizationID); err != nil {
		if errors.Is(err, store.ErrNoMembership) {
			return TokenResponse{}, o.endSession(ctx, session.ID)
		}

		return TokenResponse{}, serverErr(err)
	}

	response, tokens, err := o.mint(session, now)
	if err != nil {
		return TokenResponse{}, serverErr(err)
	}
	switch err := o.store.RotateRefresh(ctx, hash, now, tokens); {
	case errors.Is(err, store.ErrOAuthTokenReused):
		return TokenResponse{}, o.reused(ctx, session.ID)
	case errors.Is(err, store.ErrOAuthTokenUnknown), errors.Is(err, store.ErrAgentSessionUnknown):
		return TokenResponse{}, invalidGrant()
	case err != nil:
		return TokenResponse{}, serverErr(err)
	}

	return response, nil
}

// within reports whether every scope requested is one the session holds.
func within(requested oauth.Requested, grants oauth.Grants) bool {
	return grants.Access.Allows(requested.Access) &&
		(!requested.Passwords || grants.Passwords) && (!requested.SQLWrites || grants.SQLWrites)
}

// reused ends the session a replayed refresh token belongs to.
func (o *OAuth) reused(ctx context.Context, sessionID string) *OAuthError {
	log.FromContext(ctx).WarnContext(ctx, "a retired refresh token was used again, the agent session was ended")

	return o.endSession(ctx, sessionID)
}

// endSession deletes a session and answers invalid_grant.
func (o *OAuth) endSession(ctx context.Context, sessionID string) *OAuthError {
	if err := o.store.DeleteAgentSession(ctx, sessionID); err != nil {
		return serverErr(err)
	}

	return invalidGrant()
}

// Revoke ends the session a token belongs to, whichever of its tokens this is.
// A token nobody issued is not an error, as RFC 7009 asks. A token issued to
// another client is refused.
func (o *OAuth) Revoke(ctx context.Context, from string, req RevokeRequest) *OAuthError {
	if !o.tokenRequests.allow(from) {
		return tooManyRequests()
	}
	if req.Token == "" {
		return oauthErr("invalid_request", "The token parameter is required.")
	}

	_, session, err := o.store.TokenSession(ctx, auth.HashToken(req.Token))
	if errors.Is(err, store.ErrOAuthTokenUnknown) {
		return nil
	}
	if err != nil {
		return serverErr(err)
	}
	if req.ClientID != "" && req.ClientID != session.ClientID {
		return oauthErr("invalid_client", "That token was issued to another app.")
	}
	if err := o.store.DeleteAgentSession(ctx, session.ID); err != nil {
		return serverErr(err)
	}

	return nil
}

// AgentCaller is who an access token resolves to, and what it may do now.
type AgentCaller struct {
	// Caller is the in-process caller the MCP registry and the API act for.
	Caller auth.Caller
	// SessionID names the agent session, for the list and for revoking it.
	SessionID string
	// Scopes are the session's grants as they stand now: the lowest of what
	// was granted and what the Organization allows today.
	Scopes []string
	// Passwords and SQLWrites are the two permissions beyond the tier.
	Passwords  bool
	SQLWrites  bool
	ClientID   string
	ClientName string
	ClientHost string
	ClientKind string
}

// Authenticate resolves an access token presented on a path. It refuses a
// token on any path but the MCP endpoint and the credential route, and a token
// that is unknown, expired, revoked, from a session that ended, or from a
// person who no longer belongs where the agent was bound.
func (o *OAuth) Authenticate(ctx context.Context, raw, path string) (AgentCaller, error) {
	if raw == "" {
		return AgentCaller{}, ErrTokenInvalid
	}

	token, session, err := o.store.TokenSession(ctx, auth.HashToken(raw))
	if errors.Is(err, store.ErrOAuthTokenUnknown) {
		return AgentCaller{}, ErrTokenInvalid
	}
	if err != nil {
		return AgentCaller{}, fmt.Errorf("read token: %w", err)
	}

	now := o.now()
	if token.Kind != store.TokenAccess || !now.Before(token.ExpiresAt) || !o.alive(session, now) {
		return AgentCaller{}, ErrTokenInvalid
	}
	if !oauth.TokenRoute(path) {
		return AgentCaller{}, ErrTokenAudience
	}

	placement, err := o.people.Membership(ctx, session.UserID, session.OrganizationID)
	if errors.Is(err, store.ErrNoMembership) {
		return AgentCaller{}, ErrTokenInvalid
	}
	if err != nil {
		return AgentCaller{}, fmt.Errorf("read membership: %w", err)
	}
	team, err := o.resolveTeam(ctx, placement, session.TeamID)
	if errors.Is(err, ErrConsentTarget) {
		return AgentCaller{}, ErrTokenInvalid
	}
	if err != nil {
		return AgentCaller{}, err
	}

	settings, err := o.limits.AI(ctx, session.OrganizationID)
	if err != nil {
		if errors.Is(err, store.ErrOrganizationUnknown) {
			return AgentCaller{}, ErrTokenInvalid
		}

		return AgentCaller{}, fmt.Errorf("read organization limits: %w", err)
	}

	if now.Sub(session.LastUsedAt) >= agentTouchInterval {
		if err := o.store.TouchAgentSession(ctx, session.ID, now); err != nil {
			log.FromContext(ctx).WarnContext(ctx, "agent session use was not recorded", "err", err)
		}
	}

	agent := callerFor(session, placement, team, settings)
	if oauth.CredentialRoute(path) && !agent.Passwords {
		return AgentCaller{}, ErrTokenScope
	}

	return agent, nil
}

func callerFor(session store.AgentSession, placement store.Placement, team auth.Team, settings store.AISettings) AgentCaller {
	grants := oauth.Grants{
		Access:    auth.Lowest(auth.Access(session.Access), CallerAccess(auth.Access(settings.Access), placement.Role)),
		Passwords: session.Passwords && settings.Passwords,
		SQLWrites: session.SQLWrites && settings.SQLWrites,
	}
	agent := oauth.AgentName(session.ClientName, session.ClientHost)

	subject := fmt.Sprintf("user:%d", placement.UserID)
	method := store.SignInOIDC
	if session.Method != store.SignInOIDC {
		subject, method = placement.Login, store.SignInPassword
	}

	return AgentCaller{
		Caller: auth.Caller{
			Identity: auth.Identity{
				Subject: subject,
				Scope: auth.Scope{
					UserID: placement.UserID, UserName: placement.Username, OrganizationID: placement.OrganizationID,
					OrganizationName: placement.OrganizationName, Role: placement.Role,
					Team: team,
				},
				Client: auth.Client{Kind: auth.ClientMCP, Agent: agent},
				Method: method,
			},
			Access: grants.Access,
			Agent:  agent,
		},
		SessionID: session.ID, Scopes: grants.Scopes(), Passwords: grants.Passwords, SQLWrites: grants.SQLWrites,
		ClientID: session.ClientID, ClientName: session.ClientName, ClientHost: session.ClientHost, ClientKind: session.ClientKind,
	}
}
