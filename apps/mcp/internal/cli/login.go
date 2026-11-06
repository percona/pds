package cli

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/percona/pds/apps/mcp/internal/config"
	"github.com/percona/pds/apps/mcp/internal/messages"
	"github.com/percona/pds/apps/mcp/internal/oauth"
	"github.com/percona/pds/apps/mcp/internal/tokenstore"
)

// accessScopes are the scopes each access level asks for. The person can allow less on the consent
// screen, never more.
var accessScopes = map[string][]string{
	"read":   {"pds:read", "offline_access"},
	"change": {"pds:read", "pds:change", "offline_access"},
	"delete": {"pds:read", "pds:change", "pds:delete", "offline_access"},
}

// grantScopes are the permissions beyond an access level that a person can ask for. The control
// plane's Organization has to allow them, and the consent screen offers them only then.
var grantScopes = map[string]string{"passwords": "pds:credentials", "sql-write": "pds:sql_write"}

type loginFlags struct {
	access    string
	noBrowser bool
	passwords bool
	sqlWrite  bool
}

// scopes are what the sign in asks for. Writing SQL runs on a stored password, so asking for it
// asks for passwords too.
func (f loginFlags) scopes() []string {
	base := accessScopes[f.access]
	scopes := append([]string(nil), base[:len(base)-1]...)
	if f.passwords || f.sqlWrite {
		scopes = append(scopes, grantScopes["passwords"])
	}
	if f.sqlWrite {
		scopes = append(scopes, grantScopes["sql-write"])
	}

	return append(scopes, base[len(base)-1])
}

// parseLogin reads login's flags. Only these exist, so a mistyped one is refused rather than
// ignored.
func parseLogin(args []string) (flags loginFlags, ok bool) {
	flags.access = "read"
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--no-browser":
			flags.noBrowser = true
		case args[i] == "--passwords":
			flags.passwords = true
		case args[i] == "--sql-write":
			flags.sqlWrite = true
		case args[i] == "--access" && i+1 < len(args):
			i++
			flags.access = args[i]
		case strings.HasPrefix(args[i], "--access="):
			flags.access = strings.TrimPrefix(args[i], "--access=")
		default:
			return loginFlags{}, false
		}
	}
	_, known := accessScopes[flags.access]

	return flags, known
}

func runLogin(ctx context.Context, env Env, cfg config.Config, args []string) int {
	flags, ok := parseLogin(args)
	if !ok {
		return usageError(env, messages.LoginUsage)
	}
	logins, err := store(env)
	if err != nil {
		_, _ = fmt.Fprintln(env.Stderr, err)

		return 1
	}

	hc := oauth.NewHTTPClient(cfg.Origin, userAgent(env), requestTimeout)
	discoverCtx, cancel := context.WithTimeout(ctx, oauth.DiscoveryTimeout)
	server, err := oauth.Discover(discoverCtx, hc, cfg.Origin)
	cancel()
	if err != nil {
		_, _ = fmt.Fprintln(env.Stderr, signInFailure(err))

		return 1
	}

	opts := oauth.LoginOptions{Server: server, HTTP: hc, Scopes: flags.scopes()}
	opts.Announce = func(address string) { announce(env.Stdout, address, flags.noBrowser) }
	if !flags.noBrowser {
		opts.Open = env.OpenBrowser
	}
	tokens, err := oauth.Login(ctx, opts)
	if err != nil {
		_, _ = fmt.Fprintln(env.Stderr, signInFailure(err))

		return 1
	}

	// Only the refresh token is kept, and the control plane's grant is recorded with it. The access
	// token was good for minutes and is dropped here.
	if err := logins.Save(server.Issuer, tokenstore.Login{RefreshToken: tokens.RefreshToken, Scope: tokens.Scope}); err != nil {
		_, _ = fmt.Fprintln(env.Stderr, messages.LoginUnreadable)

		return 1
	}
	_, _ = fmt.Fprintln(env.Stdout, messages.LoginDone)
	// The person can allow less on the consent screen, and the Organization can allow less than they
	// asked for. Say so now, not when a tool needs it.
	granted := strings.Fields(tokens.Scope)
	passwords := (flags.passwords || flags.sqlWrite) && !slices.Contains(granted, grantScopes["passwords"])
	sqlWrites := flags.sqlWrite && !slices.Contains(granted, grantScopes["sql-write"])
	if passwords || sqlWrites {
		_, _ = fmt.Fprintln(env.Stdout, messages.LoginNarrower(passwords, sqlWrites))
	}

	return 0
}

func announce(out io.Writer, address string, noBrowser bool) {
	lead := messages.LoginOpening
	if noBrowser {
		lead = messages.LoginPrinted
	}
	_, _ = fmt.Fprintf(out, "%s\n\n%s\n\n%s\n", lead, address, messages.LoginWaiting)
}

func runLogout(ctx context.Context, env Env, cfg config.Config, args []string) int {
	if len(args) > 0 {
		return usageError(env, messages.LogoutUsage)
	}
	logins, err := store(env)
	if err != nil {
		_, _ = fmt.Fprintln(env.Stderr, err)

		return 1
	}

	login, err := logins.Load(cfg.Origin)
	if err != nil {
		_, _ = fmt.Fprintln(env.Stdout, messages.LogoutNotNeeded)

		return 0
	}

	// The saved login goes first: signing out locally must not depend on the network.
	if err := logins.Delete(cfg.Origin); err != nil {
		_, _ = fmt.Fprintln(env.Stderr, messages.LoginUnreadable)

		return 1
	}

	if !endSession(ctx, env, cfg, login.RefreshToken) {
		_, _ = fmt.Fprintln(env.Stderr, messages.LogoutRemote)

		return 0
	}
	_, _ = fmt.Fprintln(env.Stdout, messages.LogoutDone)

	return 0
}

// endSession asks the control plane to end the session, and reports whether it did.
func endSession(ctx context.Context, env Env, cfg config.Config, refreshToken string) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*requestTimeout)
	defer cancel()

	hc := oauth.NewHTTPClient(cfg.Origin, userAgent(env), requestTimeout)
	server, err := oauth.Discover(ctx, hc, cfg.Origin)
	if err != nil {
		return false
	}

	return oauth.Revoke(ctx, server, hc, refreshToken) == nil
}
