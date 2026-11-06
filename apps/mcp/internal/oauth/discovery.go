package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// LocalClientID is the identity of this program to the control plane. The control plane has it
// built in and never fetches it, and it receives a response on a loopback address only.
const LocalClientID = "pds-mcp"

// ErrNotServed means the control plane has no MCP endpoint: the feature is off, or the address is
// not a control plane.
var ErrNotServed = errors.New("the control plane does not serve MCP")

// Server is a control plane's authorization server, as its metadata describes it.
type Server struct {
	Issuer                string
	AuthorizationEndpoint string
	TokenEndpoint         string
	RevocationEndpoint    string
	// Resource is the canonical address of the MCP endpoint, sent on every token request.
	Resource string
}

type resourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
}

type serverMetadata struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RevocationEndpoint    string   `json:"revocation_endpoint"`
	ResponseTypes         []string `json:"response_types_supported"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
}

// Discover reads the control plane's protected resource and authorization server metadata. Every
// address in them has to be on the control plane's own origin, and the issuer has to be that
// origin: a refresh token is good at the issuer that gave it, and it is only ever sent there.
func Discover(ctx context.Context, hc *http.Client, origin string) (Server, error) {
	var resource resourceMetadata
	if err := getJSON(ctx, hc, origin+"/.well-known/oauth-protected-resource/mcp", &resource); err != nil {
		return Server{}, err
	}
	if strings.TrimRight(resource.Resource, "/") != origin+"/mcp" || !slices.Contains(resource.AuthorizationServers, origin) {
		return Server{}, errors.New("the control plane's MCP endpoint is not signed in through its own authorization server")
	}

	var meta serverMetadata
	if err := getJSON(ctx, hc, origin+"/.well-known/oauth-authorization-server", &meta); err != nil {
		return Server{}, err
	}
	if meta.Issuer != origin {
		return Server{}, errors.New("the authorization server's issuer is not the control plane")
	}
	if !slices.Contains(meta.CodeChallengeMethods, "S256") || !slices.Contains(meta.ResponseTypes, "code") {
		return Server{}, errors.New("the authorization server does not offer the code flow with S256")
	}
	for _, endpoint := range []string{meta.AuthorizationEndpoint, meta.TokenEndpoint} {
		if !onOrigin(endpoint, origin) {
			return Server{}, errors.New("the authorization server names an endpoint outside the control plane")
		}
	}
	if meta.RevocationEndpoint != "" && !onOrigin(meta.RevocationEndpoint, origin) {
		return Server{}, errors.New("the authorization server names an endpoint outside the control plane")
	}

	return Server{
		Issuer: origin, AuthorizationEndpoint: meta.AuthorizationEndpoint, TokenEndpoint: meta.TokenEndpoint,
		RevocationEndpoint: meta.RevocationEndpoint, Resource: origin + "/mcp",
	}, nil
}

func onOrigin(raw, origin string) bool {
	u, err := url.Parse(raw)

	return err == nil && u.User == nil && u.Scheme+"://"+u.Host == origin
}

func getJSON(ctx context.Context, hc *http.Client, address string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return fmt.Errorf("build the request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("read the control plane's sign in settings: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return ErrNotServed
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("read the control plane's sign in settings: status %d", resp.StatusCode)
	}
	body, err := readBody(resp.Body)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, into); err != nil {
		return errors.New("the control plane's sign in settings are not valid JSON")
	}

	return nil
}
