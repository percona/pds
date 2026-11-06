// Package publicip finds the address the internet sees this computer at, for a database's list of
// allowed addresses. The lookup is one HTTPS request to one documented service that answers with
// nothing but the caller's address. It sends no identifying header, and it fails rather than
// guess: anything that is not one public IPv4 address is an error, so a proxy's page or a private
// address can never end up in an allow list.
package publicip

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// Endpoint is the service asked for the address. It is run by AWS, answers in plain text, and is
// the only place this program sends anything about this computer's address.
const Endpoint = "https://checkip.amazonaws.com"

// ErrUnavailable means the address could not be found.
var ErrUnavailable = errors.New("the public address could not be found")

// maxAnswer is the longest answer read. An address is 15 characters.
const maxAnswer = 64

// NewClient returns the client the lookup uses: it takes no proxy, follows no redirect and gives
// up after a few seconds. A proxy would answer with its own address.
func NewClient() *http.Client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = nil

	return NoRedirects(&http.Client{Transport: base, Timeout: 5 * time.Second})
}

// NoRedirects returns a copy of the client that follows no redirect.
func NoRedirects(client *http.Client) *http.Client {
	copied := *client
	copied.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	return &copied
}

// shared is the carrier grade NAT range, which is not a public address.
var shared = netip.MustParsePrefix("100.64.0.0/10")

// Lookup asks the endpoint for this computer's address and returns it as a /32.
func Lookup(ctx context.Context, client *http.Client, endpoint string) (string, error) {
	if !strings.HasPrefix(endpoint, "https://") {
		return "", ErrUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", ErrUnavailable
	}
	req.Header.Set("User-Agent", "pds-mcp")

	resp, err := client.Do(req)
	if err != nil {
		return "", ErrUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", ErrUnavailable
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err != nil || len(body) > maxAnswer {
		return "", ErrUnavailable
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(string(body)))
	if err != nil || !addr.Is4() || !addr.IsGlobalUnicast() || addr.IsPrivate() || shared.Contains(addr) {
		return "", ErrUnavailable
	}

	return addr.String() + "/32", nil
}
