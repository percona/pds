package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// DefaultOIDCScopes is what the console asks the issuer for when none are set.
var DefaultOIDCScopes = []string{oidc.ScopeOpenID, "email", "profile"}

// OIDCConfig is what the adapter needs to act as a confidential client of one
// issuer.
type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	// Scopes defaults to DefaultOIDCScopes.
	Scopes []string
}

// OIDCClaims is the part of an ID token the adapter reads. The claims are
// fixed: there is no mapping setting and no organization claim, because the
// issuer proves who someone is and the adapter database decides what they
// may do.
type OIDCClaims struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
}

// OIDCFlow is what one sign in must remember between leaving for the issuer
// and coming back. It goes into a short lived signed cookie, so no replica
// needs to hold it.
type OIDCFlow struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	// Invite is the token of the invitation link the person followed, kept
	// across the trip so the claim is made with the verified address.
	Invite string `json:"i,omitempty"`
	// Organization is the name the person typed on the sign in page, used
	// only if this sign in creates an Organization.
	Organization string `json:"o,omitempty"`
}

// oidcConnections maps the providers the console offers to the issuer's own
// connection names, so a request can name only one of these.
var oidcConnections = map[string]string{
	"google": "google-oauth2",
	"github": "github",
}

// OIDCConnection returns the issuer's connection for a provider the console
// offers, and reports whether it offers it.
func OIDCConnection(provider string) (string, bool) {
	connection, ok := oidcConnections[provider]

	return connection, ok
}

// OIDCClient runs the authorization code flow with PKCE S256 against one
// issuer. Tokens are used once in Complete and discarded.
type OIDCClient struct {
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
	issuer   string
	// endSession is the issuer's RP-initiated logout endpoint, or empty when
	// it publishes none.
	endSession string
}

// NewOIDCClient discovers the issuer. It fails when discovery does, which is
// what lets the caller fall back to the password login.
func NewOIDCClient(ctx context.Context, cfg OIDCConfig) (*OIDCClient, error) {
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, errors.New("OIDC_CLIENT_ID and OIDC_CLIENT_SECRET are required with OIDC_ISSUER")
	}

	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("discover OIDC issuer: %w", err)
	}

	var metadata struct {
		EndSessionEndpoint string `json:"end_session_endpoint"`
	}
	if err := provider.Claims(&metadata); err != nil {
		return nil, fmt.Errorf("read OIDC issuer metadata: %w", err)
	}

	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = DefaultOIDCScopes
	}

	client := &OIDCClient{
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     provider.Endpoint(),
			Scopes:       scopes,
		},
		verifier:   provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		issuer:     cfg.Issuer,
		endSession: metadata.EndSessionEndpoint,
	}
	return client, nil
}

// Issuer is the issuer URL the client was configured with.
func (c *OIDCClient) Issuer() string { return c.issuer }

// Start returns the issuer URL to send the browser to, and the flow to
// remember until it comes back to redirectURL.
func (c *OIDCClient) Start(redirectURL, connection string) (string, OIDCFlow, error) {
	state, err := randomString()
	if err != nil {
		return "", OIDCFlow{}, err
	}
	nonce, err := randomString()
	if err != nil {
		return "", OIDCFlow{}, err
	}
	flow := OIDCFlow{State: state, Nonce: nonce, Verifier: oauth2.GenerateVerifier()}

	cfg := c.oauth
	cfg.RedirectURL = redirectURL

	options := []oauth2.AuthCodeOption{
		oauth2.S256ChallengeOption(flow.Verifier),
		oidc.Nonce(flow.Nonce),
	}
	if connection != "" {
		options = append(options, oauth2.SetAuthURLParam("connection", connection))
	}

	return cfg.AuthCodeURL(flow.State, options...), flow, nil
}

// Complete exchanges the code and returns the verified claims. The ID token
// must carry this flow's nonce.
func (c *OIDCClient) Complete(ctx context.Context, redirectURL, code string, flow OIDCFlow) (OIDCClaims, error) {
	cfg := c.oauth
	cfg.RedirectURL = redirectURL

	token, err := cfg.Exchange(ctx, code, oauth2.VerifierOption(flow.Verifier))
	if err != nil {
		return OIDCClaims{}, fmt.Errorf("exchange authorization code: %w", err)
	}

	raw, ok := token.Extra("id_token").(string)
	if !ok || raw == "" {
		return OIDCClaims{}, errors.New("the issuer returned no ID token")
	}

	idToken, err := c.verifier.Verify(ctx, raw)
	if err != nil {
		return OIDCClaims{}, fmt.Errorf("verify ID token: %w", err)
	}
	if idToken.Nonce != flow.Nonce {
		return OIDCClaims{}, errors.New("the ID token answers a different sign in")
	}

	var claims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return OIDCClaims{}, fmt.Errorf("read ID token claims: %w", err)
	}

	return OIDCClaims{
		Subject:       idToken.Subject,
		Email:         strings.TrimSpace(claims.Email),
		EmailVerified: claims.EmailVerified,
		Name:          strings.TrimSpace(claims.Name),
	}, nil
}

// LogoutURL is where to send the browser to end the issuer's session, which
// returns to returnTo afterwards. It is empty when the issuer publishes no
// end session endpoint.
func (c *OIDCClient) LogoutURL(returnTo string) string {
	if c.endSession == "" {
		return ""
	}

	u, err := url.Parse(c.endSession)
	if err != nil {
		return ""
	}
	q := u.Query()
	q.Set("client_id", c.oauth.ClientID)
	q.Set("post_logout_redirect_uri", returnTo)
	u.RawQuery = q.Encode()

	return u.String()
}

func randomString() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(b), nil
}
