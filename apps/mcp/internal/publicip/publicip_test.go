package publicip_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/percona/pds/apps/mcp/internal/publicip"
)

func serve(t *testing.T, status int, body string) (*httptest.Server, *http.Request) {
	t.Helper()
	var seen http.Request
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = *r.Clone(r.Context())
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	return server, &seen
}

func TestThePublicAddressComesBackAsASlash32(t *testing.T) {
	server, seen := serve(t, http.StatusOK, "203.0.113.7\n")

	got, err := publicip.Lookup(t.Context(), server.Client(), server.URL)
	if err != nil || got != "203.0.113.7/32" {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, header := range []string{"Authorization", "Cookie", "X-Forwarded-For"} {
		if seen.Header.Get(header) != "" {
			t.Fatalf("the request carried %s", header)
		}
	}
}

func TestAnythingThatIsNotOnePublicIPv4AddressFailsClosed(t *testing.T) {
	for name, body := range map[string]string{
		"private":   "10.0.0.5",
		"loopback":  "127.0.0.1",
		"link":      "169.254.1.1",
		"shared":    "100.64.0.1",
		"any":       "0.0.0.0",
		"ipv6":      "2001:db8::1",
		"two lines": "203.0.113.7\n198.51.100.1",
		"html":      "<html>blocked</html>",
		"empty":     "",
		"huge":      strings.Repeat("1", 4096),
		"cidr":      "0.0.0.0/0",
	} {
		t.Run(name, func(t *testing.T) {
			server, _ := serve(t, http.StatusOK, body)
			if got, err := publicip.Lookup(t.Context(), server.Client(), server.URL); err == nil {
				t.Fatalf("answered %q for %q", got, body)
			}
		})
	}
}

func TestAFailedLookupIsAnErrorNotAGuess(t *testing.T) {
	server, _ := serve(t, http.StatusServiceUnavailable, "203.0.113.7")
	if _, err := publicip.Lookup(t.Context(), server.Client(), server.URL); !errors.Is(err, publicip.ErrUnavailable) {
		t.Fatalf("error %v", err)
	}
}

func TestARedirectIsNotFollowed(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("203.0.113.7")) }))
	defer target.Close()
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirect.Close()

	client := redirect.Client()
	client.CheckRedirect = nil
	if _, err := publicip.Lookup(t.Context(), publicip.NoRedirects(client), redirect.URL); err == nil {
		t.Fatal("a redirect was followed")
	}
}

func TestOnlyHTTPSEndpointsAreUsed(t *testing.T) {
	if _, err := publicip.Lookup(t.Context(), http.DefaultClient, "http://checkip.example.com"); !errors.Is(err, publicip.ErrUnavailable) {
		t.Fatalf("error %v", err)
	}
}
