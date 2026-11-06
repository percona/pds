// Package oauth holds the pure rules of the control plane's OAuth
// authorization server: PKCE, redirect URI matching, scopes, resource
// identifiers and the cleaning of names a client chose for itself. Nothing
// here reads a request or a database, so every rule is testable alone.
package oauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/percona/pds/adapter/internal/auth"
)

// MaxRedirectURILength caps one redirect URI, registered or requested.
const MaxRedirectURILength = 2048

// ValidateRedirectURI refuses a redirect URI a client may not register: only
// https, and http to a loopback address, are accepted, with no fragment and no
// user information. Custom schemes are refused because no supported client
// needs one and a scheme any program can claim is not a destination.
func ValidateRedirectURI(raw string) error {
	if raw == "" || len(raw) > MaxRedirectURILength {
		return errors.New("a redirect URI must be 1 to 2048 characters")
	}

	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Opaque != "" {
		return errors.New("a redirect URI must be an absolute URL")
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return errors.New("a redirect URI must not have a fragment")
	}
	if u.User != nil {
		return errors.New("a redirect URI must not have user information")
	}
	if u.Hostname() == "" {
		return errors.New("a redirect URI must name a host")
	}

	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if loopbackHost(u.Hostname()) {
			return nil
		}

		return errors.New("a redirect URI must use https unless it is a loopback address")
	default:
		return errors.New("a redirect URI must use https or a loopback address")
	}
}

// loopbackHost reports whether a host names this machine in the forms native
// clients use: localhost, 127.0.0.1 and ::1. Other 127.x addresses and
// 0.0.0.0 are not accepted, so the rule stays what RFC 8252 names.
func loopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

// LocalClientID identifies the PDS local client, the program that runs on a
// person's own computer and bridges their agents to this control plane. The
// control plane ships with it, so a request from it is never answered by
// fetching a document, and it is the only client a person may let fetch
// database passwords or write with SQL. The id names no domain and no
// organization: a control plane carries nothing of its vendor's to identify it,
// and a document client always has an https id, so the two cannot collide.
const LocalClientID = "pds-mcp"

// LocalClientName is what the local client is called on the consent screen.
const LocalClientName = "PDS local client"

// LocalClientRedirectURIs are the only places the local client receives a
// response: its loopback listener, on any port.
func LocalClientRedirectURIs() []string {
	return []string{"http://127.0.0.1/callback", "http://[::1]/callback"}
}

// IsLoopbackRedirect reports whether a redirect URI is an http address on this
// machine, which only a program on the person's own computer can receive.
func IsLoopbackRedirect(raw string) bool {
	u, err := url.Parse(raw)

	return err == nil && u.Scheme == "http" && loopbackHost(u.Hostname())
}

// RedirectAllowed reports whether a requested redirect URI is one of the
// registered ones. The match is the exact string, except for a loopback
// address, where the port is free: a native client picks it at run time. The
// scheme, host, path and query must still match, and localhost and 127.0.0.1
// are different registrations.
func RedirectAllowed(registered []string, requested string) bool {
	if ValidateRedirectURI(requested) != nil {
		return false
	}

	for _, candidate := range registered {
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(requested)) == 1 {
			return true
		}
	}

	want, err := url.Parse(requested)
	if err != nil || want.Scheme != "http" || !loopbackHost(want.Hostname()) {
		return false
	}

	for _, candidate := range registered {
		have, err := url.Parse(candidate)
		if err != nil || have.Scheme != "http" || !loopbackHost(have.Hostname()) {
			continue
		}
		if strings.EqualFold(have.Hostname(), want.Hostname()) &&
			have.EscapedPath() == want.EscapedPath() &&
			have.RawQuery == want.RawQuery {
			return true
		}
	}

	return false
}

const (
	minVerifier = 43
	maxVerifier = 128
)

var (
	verifierPattern  = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)
	challengePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
)

// ValidChallenge reports whether a code challenge is the shape of an S256
// digest: 43 characters of base64url with no padding.
func ValidChallenge(challenge string) bool { return challengePattern.MatchString(challenge) }

// VerifyPKCE reports whether the verifier is the one the S256 challenge was
// made from. The comparison takes the same time whatever the two share.
func VerifyPKCE(challenge, verifier string) bool {
	if !ValidChallenge(challenge) || len(verifier) < minVerifier || len(verifier) > maxVerifier ||
		!verifierPattern.MatchString(verifier) {
		return false
	}

	sum := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])

	return subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) == 1
}

// The scopes the server offers. A token carries the session's grants as
// these, each tier including the ones below it.
const (
	ScopeRead          = "pds:read"
	ScopeChange        = "pds:change"
	ScopeDelete        = "pds:delete"
	ScopeCredentials   = "pds:credentials"
	ScopeSQLWrite      = "pds:sql_write"
	ScopeOfflineAccess = "offline_access"
)

// ErrUnknownScope means a request named a scope this server does not offer.
var ErrUnknownScope = errors.New("that scope is not offered")

// ResourceScopes are the scopes a protected resource advertises. It leaves out
// offline_access, which only the authorization server lists.
func ResourceScopes() []string {
	return []string{ScopeRead, ScopeChange, ScopeDelete, ScopeCredentials, ScopeSQLWrite}
}

// AuthorizationServerScopes are the scopes the server metadata lists.
func AuthorizationServerScopes() []string {
	return append(ResourceScopes(), ScopeOfflineAccess)
}

// Grants is what a session may do: the highest tier, and the two extra
// permissions an Organization can allow.
type Grants struct {
	Access    auth.Access
	Passwords bool
	SQLWrites bool
}

// Scopes is the token's view of the grants, in a fixed order.
func (g Grants) Scopes() []string {
	scopes := []string{ScopeRead}
	switch g.Access {
	case auth.AccessChange:
		scopes = append(scopes, ScopeChange)
	case auth.AccessChangeDelete:
		scopes = append(scopes, ScopeChange, ScopeDelete)
	}
	if g.Passwords {
		scopes = append(scopes, ScopeCredentials)
	}
	if g.SQLWrites {
		scopes = append(scopes, ScopeSQLWrite)
	}

	return scopes
}

// Requested is what an authorization request asked for. Consent may grant
// less than this and never more.
type Requested struct {
	Access    auth.Access
	Passwords bool
	SQLWrites bool
}

// ParseScopes reads a space separated scope parameter. No scope asks for
// everything the server offers. A request naming scopes but no tier asks for
// read. offline_access is accepted and means nothing, because every grant gets
// a refresh token.
func ParseScopes(raw string) (Requested, error) {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return Requested{Access: auth.AccessChangeDelete, Passwords: true, SQLWrites: true}, nil
	}

	requested := Requested{Access: auth.AccessRead}
	for _, scope := range fields {
		switch scope {
		case ScopeRead, ScopeOfflineAccess:
		case ScopeChange:
			requested.Access = higher(requested.Access, auth.AccessChange)
		case ScopeDelete:
			requested.Access = higher(requested.Access, auth.AccessChangeDelete)
		case ScopeCredentials:
			requested.Passwords = true
		case ScopeSQLWrite:
			requested.SQLWrites = true
		default:
			return Requested{}, ErrUnknownScope
		}
	}

	return requested, nil
}

func higher(a, b auth.Access) auth.Access {
	if a.Allows(b) {
		return a
	}

	return b
}

// Allows reports whether the grants stay within what was requested.
func (r Requested) Allows(g Grants) bool {
	if !r.Access.Allows(g.Access) {
		return false
	}

	return (!g.Passwords || r.Passwords) && (!g.SQLWrites || r.SQLWrites)
}

// CanonicalResource normalizes a resource identifier so the forms clients
// type compare equal: lower case scheme and host, no default port, no
// trailing slash. A value with a query, fragment or user information is not
// a resource.
func CanonicalResource(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return "", false
	}

	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}

	return u.Scheme + "://" + host + strings.TrimRight(u.EscapedPath(), "/"), true
}

const maxNameRunes = 64

// DisplayName cleans a name a client gave itself, which is self asserted and
// shown to people: control, format and direction characters are dropped,
// whitespace collapses and the length is capped.
func DisplayName(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		switch {
		case unicode.IsSpace(r):
			b.WriteRune(' ')
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || !unicode.IsPrint(r):
		default:
			b.WriteRune(r)
		}
	}

	name := strings.Join(strings.Fields(b.String()), " ")
	if runes := []rune(name); len(runes) > maxNameRunes {
		name = strings.TrimSpace(string(runes[:maxNameRunes]))
	}

	return name
}

var disallowedAgent = regexp.MustCompile(`[^A-Za-z0-9 ._+-]+`)

// AgentName is the name the activity log keeps for a client: ASCII letters,
// digits, inner spaces and product punctuation, at most 64 characters. A name
// with nothing usable falls back to the client's host, and then to "Agent".
func AgentName(name, host string) string {
	for _, candidate := range []string{name, host, "Agent"} {
		cleaned := disallowedAgent.ReplaceAllString(DisplayName(candidate), " ")
		cleaned = strings.Join(strings.Fields(cleaned), " ")
		if len(cleaned) > 64 {
			cleaned = cleaned[:64]
		}
		cleaned = strings.Trim(cleaned, " ")
		cleaned = strings.TrimLeft(cleaned, "._+-")
		cleaned = strings.TrimRight(cleaned, " ")
		if cleaned != "" && auth.ValidAgent(cleaned) {
			return cleaned
		}
	}

	return "Agent"
}

// HostOf returns the host of an https URL, lower case, or empty.
func HostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}

	return strings.ToLower(u.Hostname())
}

// PublicAddress reports whether an address is one a document fetch may
// connect to: globally routable unicast and nothing from the special use
// ranges, including addresses that embed an IPv4 address.
func PublicAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsInterfaceLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	if addr.Is6() {
		// 6to4 and NAT64 carry an IPv4 address that could be a private one.
		if embedded, ok := embeddedV4(addr); ok && !PublicAddress(embedded) {
			return false
		}
	}

	return !slices.ContainsFunc(specialUse, func(p netip.Prefix) bool { return p.Contains(addr) })
}

func embeddedV4(addr netip.Addr) (netip.Addr, bool) {
	b := addr.As16()
	switch {
	case b[0] == 0x20 && b[1] == 0x02: // 6to4
		return netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}), true
	case netip.MustParsePrefix("64:ff9b::/96").Contains(addr): // NAT64
		return netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}), true
	default:
		return netip.Addr{}, false
	}
}

// specialUse lists the registered special use ranges not already covered by
// the netip predicates (RFC 6890 and its successors).
var specialUse = mustPrefixes(
	"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24",
	"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "255.255.255.255/32",
	"::/96", "::/128", "::1/128", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16",
	"3fff::/20", "fc00::/7", "fe80::/10", "ff00::/8",
)

func mustPrefixes(list ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(list))
	for _, s := range list {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			panic(fmt.Sprintf("bad prefix %q: %v", s, err))
		}
		out = append(out, p)
	}

	return out
}

var credentialRoute = regexp.MustCompile(`^/api/databases/[^/]+/[^/]+/credential$`)

// CredentialRoute reports whether a path is the database credential route.
func CredentialRoute(path string) bool {
	if !credentialRoute.MatchString(path) {
		return false
	}

	return !slices.Contains(strings.Split(path, "/"), "..") && !slices.Contains(strings.Split(path, "/"), ".")
}

// TokenRoute reports whether an access token may be presented on a path: the
// MCP endpoint and the database credential route, and nothing else. A token is
// bound to these two, so it never works as a session on the rest of the API.
func TokenRoute(path string) bool {
	return path == "/mcp" || path == "/mcp/" || CredentialRoute(path)
}
