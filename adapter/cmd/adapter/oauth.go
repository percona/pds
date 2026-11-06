package main

import (
	"log/slog"
	"net/url"
	"strings"

	"github.com/percona/pds/adapter/internal/client/clientmeta"
	"github.com/percona/pds/adapter/internal/config"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
)

// oauthIssuer is the address agents reach this control plane at, as the
// authorization server's issuer: HUB_OAUTH_ISSUER, or the public URL when that
// is not set, when it is an origin and uses https, or http on a loopback host
// for a laptop. Anything else, including an address with a path, is not usable
// as an issuer and yields the empty string, whatever the other setting says.
func oauthIssuer(cfg config.Config) string {
	raw := cfg.HubOAuthIssuer
	if strings.TrimSpace(raw) == "" {
		raw = cfg.HubPublicURL
	}
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" || u.User != nil ||
		u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.Contains(raw, "#") {
		return ""
	}

	switch u.Scheme {
	case "https":
		return raw
	case "http":
		switch strings.ToLower(u.Hostname()) {
		case "localhost", "127.0.0.1", "::1":
			return raw
		}
	}

	return ""
}

// newOAuthService builds the authorization server agents sign in through. It
// is nil when there is no database to keep sessions in or no public address to
// be an issuer at, and its endpoints then answer 404.
func newOAuthService(
	cfg config.Config,
	db *store.Starter,
	identity *service.Identity,
	limits service.AccessLimits,
	members service.OAuthMembers,
	recorder service.ActivityRecorder,
) *service.OAuth {
	issuer := oauthIssuer(cfg)
	if issuer == "" || db == nil || identity == nil {
		slog.Warn("the OAuth endpoints are off: HUB_OAUTH_ISSUER, or HUB_PUBLIC_URL without it, must be an https origin with no path, or http on localhost")

		return nil
	}

	return service.NewOAuth(
		store.NewLazyOAuth(db), identity, limits, clientmeta.New(), issuer,
		service.WithAgentSessionLifetime(cfg.SessionLifetime),
		service.WithAgentSessionIdle(cfg.SessionIdle),
		service.WithOAuthMembers(members),
		service.WithOAuthRecorder(recorder),
	)
}
