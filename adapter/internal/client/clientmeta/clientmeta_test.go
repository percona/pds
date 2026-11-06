package clientmeta_test

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/clientmeta"
)

const clientID = "https://agent.example/oauth/client.json"

// fakeResolver answers a fixed set of addresses and counts the lookups.
type fakeResolver struct {
	addrs []netip.Addr
	err   error
	calls atomic.Int32
}

func (f *fakeResolver) LookupNetIP(_ context.Context, _, _ string) ([]netip.Addr, error) {
	f.calls.Add(1)

	return f.addrs, f.err
}

func addrs(list ...string) []netip.Addr {
	out := make([]netip.Addr, 0, len(list))
	for _, s := range list {
		out = append(out, netip.MustParseAddr(s))
	}

	return out
}

// rig is a fetcher wired to an HTTPS test server and a resolver the test
// controls. The dialer records the address it was asked for and connects to
// the test server whatever it was.
type rig struct {
	fetcher  *clientmeta.Fetcher
	resolver *fakeResolver
	server   *httptest.Server
	hits     atomic.Int32
	mu       sync.Mutex
	dialed   []string
	now      time.Time
	handler  func(w http.ResponseWriter, r *http.Request)
}

func newRig(t *testing.T, handler func(w http.ResponseWriter, r *http.Request), opts ...clientmeta.Option) *rig {
	t.Helper()

	r := &rig{
		resolver: &fakeResolver{addrs: addrs("93.184.216.34")},
		now:      time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC),
		handler:  handler,
	}
	r.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.hits.Add(1)
		r.handler(w, req)
	}))
	t.Cleanup(r.server.Close)

	pool := r.server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	all := append([]clientmeta.Option{
		clientmeta.WithResolver(r.resolver),
		clientmeta.WithDialer(func(ctx context.Context, network, addr string) (net.Conn, error) {
			r.mu.Lock()
			r.dialed = append(r.dialed, addr)
			r.mu.Unlock()

			return (&net.Dialer{}).DialContext(ctx, network, r.server.Listener.Addr().String())
		}),
		clientmeta.WithTLSConfig(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12, ServerName: "example.com"}),
		clientmeta.WithClock(func() time.Time { return r.now }),
	}, opts...)
	r.fetcher = clientmeta.New(all...)

	return r
}

func (r *rig) addressesDialed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]string(nil), r.dialed...)
}

func document(id string) string {
	return fmt.Sprintf(`{"client_id":%q,"client_name":"Agent","redirect_uris":["http://localhost/callback"],`+
		`"grant_types":["authorization_code","refresh_token","urn:ietf:params:oauth:grant-type:jwt-bearer"],`+
		`"token_endpoint_auth_method":"none"}`, id)
}

func serveJSON(body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

func TestFetchReadsAValidDocumentAndIgnoresGrantTypesItDoesNotKnow(t *testing.T) {
	r := newRig(t, serveJSON(document(clientID)))

	doc, err := r.fetcher.Fetch(t.Context(), clientID)

	require.NoError(t, err)
	assert.Equal(t, clientID, doc.ClientID)
	assert.Equal(t, "Agent", doc.ClientName)
	assert.Equal(t, []string{"http://localhost/callback"}, doc.RedirectURIs)
}

func TestFetchSendsNoCredentialsAndAsksForJSON(t *testing.T) {
	var got http.Header
	var method string
	r := newRig(t, func(w http.ResponseWriter, req *http.Request) {
		got, method = req.Header.Clone(), req.Method
		serveJSON(document(clientID))(w, req)
	})

	_, err := r.fetcher.Fetch(t.Context(), clientID)

	require.NoError(t, err)
	assert.Equal(t, http.MethodGet, method)
	assert.Contains(t, got.Get("Accept"), "application/json")
	assert.Empty(t, got.Get("Cookie"))
	assert.Empty(t, got.Get("Authorization"))
}

func TestClientIDsThatAreNotSafeDocumentAddressesAreRefusedWithoutAFetch(t *testing.T) {
	r := newRig(t, serveJSON(document(clientID)))

	for _, id := range []string{
		"http://agent.example/oauth/client.json",
		"https://agent.example",
		"https://agent.example/",
		"https://agent.example/a?x=1",
		"https://agent.example/a#frag",
		"https://user@agent.example/a",
		"https://agent.example:8443/a",
		"https://agent.example/a/../b",
		"https://agent.example/a/./b",
		"https://93.184.216.34/a",
		"https://[2606:4700::1]/a",
		"https://localhost/a",
		"https:///a",
		"ftp://agent.example/a",
		"agent.example/a",
		"https://agent.example/" + strings.Repeat("a", 2100),
		"https://b\u00fccher.example/client.json",
		"https://xn--bcher-kva.example\u202e/client.json",
		"",
	} {
		_, err := r.fetcher.Fetch(t.Context(), id)
		require.ErrorIs(t, err, clientmeta.ErrClientID, id)
	}

	assert.Zero(t, r.resolver.calls.Load(), "no name was looked up")
	assert.Zero(t, r.hits.Load())
}

func TestADocumentOnAPrivateAddressIsRefusedBeforeAnyConnection(t *testing.T) {
	for _, address := range []string{
		"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "0.0.0.0",
		"198.18.0.1", "224.0.0.1", "240.0.0.1", "::1", "fd00::1", "fe80::1", "::ffff:10.0.0.1", "::ffff:127.0.0.1",
		"64:ff9b::a00:1", "2002:0a00:0001::1", "2001:db8::1", "::",
	} {
		r := newRig(t, serveJSON(document(clientID)))
		r.resolver.addrs = addrs(address)

		_, err := r.fetcher.Fetch(t.Context(), clientID)

		require.ErrorIs(t, err, clientmeta.ErrNotPublic, address)
		assert.Empty(t, r.addressesDialed(), "%s was dialed", address)
		assert.Zero(t, r.hits.Load())
	}
}

func TestAHostWithAnyPrivateAddressIsRefused(t *testing.T) {
	r := newRig(t, serveJSON(document(clientID)))
	r.resolver.addrs = addrs("93.184.216.34", "10.0.0.9")

	_, err := r.fetcher.Fetch(t.Context(), clientID)

	require.ErrorIs(t, err, clientmeta.ErrNotPublic)
	assert.Empty(t, r.addressesDialed())
}

func TestTheConnectionGoesToTheAddressThatWasChecked(t *testing.T) {
	r := newRig(t, serveJSON(document(clientID)))
	r.resolver.addrs = addrs("93.184.216.34")

	_, err := r.fetcher.Fetch(t.Context(), clientID)

	require.NoError(t, err)
	assert.Equal(t, []string{"93.184.216.34:443"}, r.addressesDialed(),
		"the dial uses the checked address, never the name")
	assert.Equal(t, int32(1), r.resolver.calls.Load(), "the name is resolved once, so a second answer cannot move it")
}

func TestAResolverFailureIsAFetchFailure(t *testing.T) {
	r := newRig(t, serveJSON(document(clientID)))
	r.resolver.addrs, r.resolver.err = nil, errors.New("no such host")

	_, err := r.fetcher.Fetch(t.Context(), clientID)

	require.ErrorIs(t, err, clientmeta.ErrFetch)
}

func TestARedirectIsNotFollowed(t *testing.T) {
	r := newRig(t, func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "https://169.254.169.254/latest/meta-data", http.StatusFound)
	})

	_, err := r.fetcher.Fetch(t.Context(), clientID)

	require.ErrorIs(t, err, clientmeta.ErrFetch)
	assert.Equal(t, int32(1), r.hits.Load())
	assert.Len(t, r.addressesDialed(), 1)
}

func TestOnlyA200IsAccepted(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusNotFound, http.StatusInternalServerError, http.StatusAccepted} {
		r := newRig(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(document(clientID)))
		})

		_, err := r.fetcher.Fetch(t.Context(), clientID)

		require.ErrorIs(t, err, clientmeta.ErrFetch, status)
	}
}

func TestADocumentOverTheSizeCapIsRefused(t *testing.T) {
	r := newRig(t, serveJSON(`{"client_id":"`+clientID+`","client_name":"`+strings.Repeat("a", 6000)+`"}`))

	_, err := r.fetcher.Fetch(t.Context(), clientID)

	require.ErrorIs(t, err, clientmeta.ErrFetch)
}

func TestASlowServerIsGivenUpOn(t *testing.T) {
	release := make(chan struct{})
	r := newRig(t, func(w http.ResponseWriter, req *http.Request) {
		select {
		case <-release:
		case <-req.Context().Done():
		}
	}, clientmeta.WithTimeout(150*time.Millisecond))
	t.Cleanup(func() { close(release) })

	start := time.Now()
	_, err := r.fetcher.Fetch(t.Context(), clientID)

	require.ErrorIs(t, err, clientmeta.ErrFetch)
	assert.Less(t, time.Since(start), 3*time.Second)
}

func TestADocumentThatIsNotAnAcceptableClientIsRefused(t *testing.T) {
	good := document(clientID)
	for name, body := range map[string]string{
		"not json":             `<html>hi</html>`,
		"an array":             `[]`,
		"another client id":    document("https://agent.example/other.json"),
		"no client id":         `{"client_name":"A","redirect_uris":["http://localhost/cb"]}`,
		"no name":              `{"client_id":"` + clientID + `","redirect_uris":["http://localhost/cb"]}`,
		"no redirects":         `{"client_id":"` + clientID + `","client_name":"A"}`,
		"empty redirects":      `{"client_id":"` + clientID + `","client_name":"A","redirect_uris":[]}`,
		"an http redirect":     `{"client_id":"` + clientID + `","client_name":"A","redirect_uris":["http://evil.example/cb"]}`,
		"a custom scheme":      `{"client_id":"` + clientID + `","client_name":"A","redirect_uris":["agent://cb"]}`,
		"a secret":             strings.Replace(good, `"token_endpoint_auth_method":"none"`, `"client_secret":"s"`, 1),
		"a secret expiry":      strings.Replace(good, `"token_endpoint_auth_method":"none"`, `"client_secret_expires_at":0`, 1),
		"a shared secret auth": strings.Replace(good, `"none"`, `"client_secret_basic"`, 1),
		"private key auth":     strings.Replace(good, `"none"`, `"private_key_jwt"`, 1),
		"redirects not text":   `{"client_id":"` + clientID + `","client_name":"A","redirect_uris":[1]}`,
		"too many redirects":   `{"client_id":"` + clientID + `","client_name":"A","redirect_uris":[` + strings.TrimSuffix(strings.Repeat(`"http://localhost/cb",`, 12), ",") + `]}`,
	} {
		r := newRig(t, serveJSON(body))

		_, err := r.fetcher.Fetch(t.Context(), clientID)

		require.ErrorIs(t, err, clientmeta.ErrDocument, name)
	}
}

func TestADocumentMustBeServedAsJSON(t *testing.T) {
	r := newRig(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(document(clientID)))
	})

	_, err := r.fetcher.Fetch(t.Context(), clientID)

	require.ErrorIs(t, err, clientmeta.ErrDocument)

	r = newRig(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/client-metadata+json; charset=utf-8")
		_, _ = w.Write([]byte(document(clientID)))
	})
	_, err = r.fetcher.Fetch(t.Context(), clientID)
	require.NoError(t, err)
}

func TestADocumentIsCachedForItsLifetimeAndFetchedAgainAfter(t *testing.T) {
	r := newRig(t, func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		serveJSON(document(clientID))(w, req)
	})

	_, err := r.fetcher.Fetch(t.Context(), clientID)
	require.NoError(t, err)
	r.now = r.now.Add(299 * time.Second)
	_, err = r.fetcher.Fetch(t.Context(), clientID)
	require.NoError(t, err)
	assert.Equal(t, int32(1), r.hits.Load(), "inside the lifetime the cached document is used")

	r.now = r.now.Add(2 * time.Second)
	_, err = r.fetcher.Fetch(t.Context(), clientID)
	require.NoError(t, err)
	assert.Equal(t, int32(2), r.hits.Load(), "past the lifetime it is fetched again")
}

func TestNoStoreStillGetsTheServersOwnShortestLifetime(t *testing.T) {
	r := newRig(t, func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Cache-Control", "no-store,no-cache,max-age=0")
		serveJSON(document(clientID))(w, req)
	})

	_, _ = r.fetcher.Fetch(t.Context(), clientID)
	_, _ = r.fetcher.Fetch(t.Context(), clientID)
	assert.Equal(t, int32(1), r.hits.Load())

	r.now = r.now.Add(61 * time.Second)
	_, _ = r.fetcher.Fetch(t.Context(), clientID)
	assert.Equal(t, int32(2), r.hits.Load())
}

func TestFailuresAreNeverCached(t *testing.T) {
	status := http.StatusInternalServerError
	r := newRig(t, func(w http.ResponseWriter, req *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)

			return
		}
		serveJSON(document(clientID))(w, req)
	})

	_, err := r.fetcher.Fetch(t.Context(), clientID)
	require.Error(t, err)
	status = http.StatusOK
	_, err = r.fetcher.Fetch(t.Context(), clientID)
	require.NoError(t, err, "the next attempt is a new fetch")
}

func TestTheCacheHoldsAFixedNumberOfDocuments(t *testing.T) {
	r := newRig(t, func(w http.ResponseWriter, req *http.Request) {
		serveJSON(document("https://agent.example"+req.URL.Path))(w, req)
	}, clientmeta.WithCacheSize(2))

	for _, path := range []string{"/a", "/b", "/c"} {
		_, err := r.fetcher.Fetch(t.Context(), "https://agent.example"+path)
		require.NoError(t, err)
	}
	_, err := r.fetcher.Fetch(t.Context(), "https://agent.example/a")
	require.NoError(t, err)

	assert.Equal(t, int32(4), r.hits.Load(), "the oldest entry was dropped to stay within the size")
}
