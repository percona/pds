// Package clientmeta fetches the Client ID Metadata Document an OAuth client
// publishes at its client_id URL. The URL is chosen by whoever sends the
// authorization request, so the fetch is built to be unable to reach anything
// but a public host: it resolves the name once, refuses every address that is
// not globally routable, connects to the address it checked, follows no
// redirect, and caps the time and the size it will read.
package clientmeta

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/percona/pds/adapter/internal/oauth"
)

// The ways a fetch fails. A caller maps them to its own answers and logs the
// wrapped cause, which can name the host.
var (
	// ErrClientID means the identifier is not a URL this server will fetch.
	ErrClientID = errors.New("that client id is not a document address")
	// ErrNotPublic means the host resolves to an address that is not public.
	ErrNotPublic = errors.New("that client id host is not on a public address")
	// ErrFetch means the document could not be read.
	ErrFetch = errors.New("the client metadata document could not be fetched")
	// ErrDocument means the document was read and is not an acceptable client.
	ErrDocument = errors.New("the client metadata document is not valid")
)

const (
	maxIDLength     = 2048
	maxBody         = 5 * 1024
	maxRedirectURIs = 10
	defaultTimeout  = 5 * time.Second
	minTTL          = time.Minute
	defaultTTL      = 5 * time.Minute
	maxTTL          = time.Hour
	defaultEntries  = 256
)

// Document is the part of a client's metadata the server uses.
type Document struct {
	ClientID   string
	ClientName string
	// RedirectURIs are the destinations the client registered, each already
	// checked to be https or loopback.
	RedirectURIs []string
}

// Resolver looks a name up. *net.Resolver is one.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Option configures New.
type Option func(*Fetcher)

// WithResolver replaces the name lookup. Tests use it to answer a name with an
// address of their choosing.
func WithResolver(r Resolver) Option { return func(f *Fetcher) { f.resolver = r } }

// WithDialer replaces the connection a checked address is dialed with.
func WithDialer(dial func(ctx context.Context, network, addr string) (net.Conn, error)) Option {
	return func(f *Fetcher) { f.dial = dial }
}

// WithTLSConfig replaces the TLS settings, so a test can trust its own server.
func WithTLSConfig(cfg *tls.Config) Option { return func(f *Fetcher) { f.tlsConfig = cfg } }

// WithClock replaces the clock the cache reads.
func WithClock(now func() time.Time) Option { return func(f *Fetcher) { f.now = now } }

// WithTimeout sets how long one fetch may take in all.
func WithTimeout(d time.Duration) Option { return func(f *Fetcher) { f.timeout = d } }

// WithCacheSize sets how many documents are kept.
func WithCacheSize(n int) Option {
	return func(f *Fetcher) {
		if n > 0 {
			f.entries = n
		}
	}
}

type entry struct {
	doc     Document
	expires time.Time
}

// Fetcher reads client metadata documents and keeps the valid ones for a short
// while. It is safe for concurrent use.
type Fetcher struct {
	resolver  Resolver
	dial      func(ctx context.Context, network, addr string) (net.Conn, error)
	tlsConfig *tls.Config
	now       func() time.Time
	timeout   time.Duration
	entries   int

	mu    sync.Mutex
	cache map[string]entry
	order []string
}

// New returns a fetcher that uses the system resolver.
func New(opts ...Option) *Fetcher {
	f := &Fetcher{
		resolver: net.DefaultResolver,
		dial:     (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
		now:      time.Now,
		timeout:  defaultTimeout,
		entries:  defaultEntries,
		cache:    make(map[string]entry),
	}
	for _, opt := range opts {
		opt(f)
	}

	return f
}

// ValidateClientID checks that an identifier is an https URL this server will
// fetch: a name with a dot, no port but 443, a path, no query, no fragment, no
// user information and no dot segments.
func ValidateClientID(id string) error {
	if id == "" || len(id) > maxIDLength || strings.Contains(id, "#") {
		return ErrClientID
	}

	u, err := url.Parse(id)
	if err != nil || u.Scheme != "https" || u.User != nil || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" {
		return ErrClientID
	}

	host := u.Hostname()
	// A name written in Unicode can look like another one when it is shown to
	// a person, so only the ASCII form (punycode for an international name) is accepted.
	for _, r := range host {
		if r > unicode.MaxASCII {
			return ErrClientID
		}
	}
	if host == "" || !strings.Contains(host, ".") || strings.HasSuffix(host, ".") {
		return ErrClientID
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return ErrClientID
	}
	if port := u.Port(); port != "" && port != "443" {
		return ErrClientID
	}
	if len(u.Path) < 2 {
		return ErrClientID
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return ErrClientID
		}
	}

	return nil
}

// Fetch returns the client's document, from the cache while it is fresh. A
// failed or invalid fetch is never cached.
func (f *Fetcher) Fetch(ctx context.Context, clientID string) (Document, error) {
	if err := ValidateClientID(clientID); err != nil {
		return Document{}, err
	}

	if doc, ok := f.cached(clientID); ok {
		return doc, nil
	}

	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()

	body, ttl, err := f.read(ctx, clientID)
	if err != nil {
		return Document{}, err
	}

	doc, err := parse(clientID, body)
	if err != nil {
		return Document{}, err
	}

	f.store(clientID, doc, ttl)

	return doc, nil
}

func (f *Fetcher) cached(id string) (Document, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	hit, ok := f.cache[id]
	if !ok {
		return Document{}, false
	}
	if !f.now().Before(hit.expires) {
		f.drop(id)

		return Document{}, false
	}

	return hit.doc, true
}

func (f *Fetcher) store(id string, doc Document, ttl time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.cache[id]; !ok {
		for len(f.order) >= f.entries {
			f.drop(f.order[0])
		}
		f.order = append(f.order, id)
	}
	f.cache[id] = entry{doc: doc, expires: f.now().Add(ttl)}
}

// drop removes one entry. The caller holds the lock.
func (f *Fetcher) drop(id string) {
	delete(f.cache, id)
	for i, held := range f.order {
		if held == id {
			f.order = append(f.order[:i], f.order[i+1:]...)

			break
		}
	}
}

// read fetches the raw document and says how long it may be kept.
func (f *Fetcher) read(ctx context.Context, clientID string) ([]byte, time.Duration, error) {
	transport := &http.Transport{
		// No proxy: one would resolve the name itself and defeat the check.
		Proxy:                 nil,
		DialContext:           f.guardedDial,
		TLSClientConfig:       f.tlsConfig,
		TLSHandshakeTimeout:   3 * time.Second,
		ResponseHeaderTimeout: f.timeout,
		DisableKeepAlives:     true,
	}
	defer transport.CloseIdleConnections()

	client := &http.Client{
		Transport: transport,
		Timeout:   f.timeout,
		// A redirect could point anywhere, so it is an answer, not a hop.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clientID, nil)
	if err != nil {
		return nil, 0, ErrClientID
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "pds-control-plane")

	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, ErrNotPublic) {
			return nil, 0, ErrNotPublic
		}

		return nil, 0, fmt.Errorf("%w: %v", ErrFetch, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("%w: status %d", ErrFetch, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %v", ErrFetch, err)
	}
	if len(body) > maxBody {
		return nil, 0, fmt.Errorf("%w: the document is over %d bytes", ErrFetch, maxBody)
	}

	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || (mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json")) {
		return nil, 0, fmt.Errorf("%w: the document is not served as JSON", ErrDocument)
	}

	return body, lifetime(resp.Header.Get("Cache-Control")), nil
}

// guardedDial resolves the host once, refuses it unless every address is
// public, and connects to one of those addresses by number. The name is never
// looked up a second time, so it cannot move to a private address between the
// check and the connection.
func (f *Fetcher) guardedDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, ErrClientID
	}

	var resolved []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		resolved = []netip.Addr{ip}
	} else if resolved, err = f.resolver.LookupNetIP(ctx, "ip", host); err != nil {
		return nil, fmt.Errorf("%w: lookup failed: %v", ErrFetch, err)
	}
	if len(resolved) == 0 {
		return nil, fmt.Errorf("%w: the host has no address", ErrFetch)
	}
	for _, ip := range resolved {
		if !oauth.PublicAddress(ip) {
			return nil, ErrNotPublic
		}
	}

	var last error
	for _, ip := range resolved {
		conn, err := f.dial(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if err == nil {
			return conn, nil
		}
		last = err
	}

	return nil, last
}

// lifetime reads max-age from Cache-Control and keeps it between the
// server's own shortest and longest. A client that says not to cache is still
// held for the shortest, because a fetch on every request is a way to make
// this server fetch.
func lifetime(header string) time.Duration {
	ttl := defaultTTL
	for _, directive := range strings.Split(header, ",") {
		name, value, _ := strings.Cut(strings.TrimSpace(strings.ToLower(directive)), "=")
		switch name {
		case "no-store", "no-cache":
			return minTTL
		case "max-age":
			if seconds, err := strconv.Atoi(strings.Trim(value, `"`)); err == nil {
				ttl = time.Duration(seconds) * time.Second
			}
		}
	}

	return min(max(ttl, minTTL), maxTTL)
}

// parse checks a document against the client id it was fetched for.
func parse(clientID string, body []byte) (Document, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return Document{}, fmt.Errorf("%w: not a JSON object", ErrDocument)
	}

	var id, name string
	if err := field(fields, "client_id", &id); err != nil || id != clientID {
		return Document{}, fmt.Errorf("%w: client_id is not the document's address", ErrDocument)
	}
	if err := field(fields, "client_name", &name); err != nil {
		return Document{}, err
	}
	name = oauth.DisplayName(name)
	if name == "" {
		return Document{}, fmt.Errorf("%w: client_name is required", ErrDocument)
	}

	var redirects []string
	if err := field(fields, "redirect_uris", &redirects); err != nil || len(redirects) == 0 || len(redirects) > maxRedirectURIs {
		return Document{}, fmt.Errorf("%w: redirect_uris must hold 1 to %d addresses", ErrDocument, maxRedirectURIs)
	}
	for _, uri := range redirects {
		if err := oauth.ValidateRedirectURI(uri); err != nil {
			return Document{}, fmt.Errorf("%w: %v", ErrDocument, err)
		}
	}

	// A public client holds no secret. A document that carries one, or asks
	// for a way to authenticate this server cannot do, is refused.
	for _, forbidden := range []string{"client_secret", "client_secret_expires_at"} {
		if _, present := fields[forbidden]; present {
			return Document{}, fmt.Errorf("%w: a client document must not hold a secret", ErrDocument)
		}
	}
	var method string
	if err := field(fields, "token_endpoint_auth_method", &method); err != nil {
		return Document{}, err
	}
	if method != "" && method != "none" {
		return Document{}, fmt.Errorf("%w: only public clients are supported", ErrDocument)
	}

	return Document{ClientID: id, ClientName: name, RedirectURIs: redirects}, nil
}

// field decodes one member when it is present, and refuses one of the wrong type.
func field(fields map[string]json.RawMessage, name string, into any) error {
	raw, ok := fields[name]
	if !ok {
		return nil
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("%w: %s has the wrong type", ErrDocument, name)
	}

	return nil
}
