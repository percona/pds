package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/percona/pds/adapter/internal/client/claude"
	"github.com/percona/pds/adapter/internal/client/cursor"
	"github.com/percona/pds/adapter/internal/client/servicenow"
)

// Key validation errors the HTTP layer maps onto the API error contract.
var (
	// ErrKeyRefused means the provider answered that the key is not valid.
	ErrKeyRefused = errors.New("the provider refused the key")
	// ErrKeyUnchecked means the provider could not be asked, or answered with
	// a failure that says nothing about the key.
	ErrKeyUnchecked = errors.New("the key could not be checked")
)

// keyCheckTimeout bounds the one call a save makes to the provider.
const keyCheckTimeout = 10 * time.Second

// WithConnectionsTransport sends the key check through transport instead of
// the default one, so tests answer for the provider.
func WithConnectionsTransport(transport http.RoundTripper) ConnectionsOption {
	return func(c *Connections) {
		c.transport = transport
	}
}

// checkKey makes one authenticated read with a key that is not stored yet.
// The provider's reply is reduced to ErrKeyRefused or ErrKeyUnchecked, and the
// key never reaches the returned error.
func (c *Connections) checkKey(ctx context.Context, kind ConnectionKind, apiKey, baseURL string) error {
	ctx, cancel := context.WithTimeout(ctx, keyCheckTimeout)
	defer cancel()

	httpClient := httpClientFromTransport(c.transport, keyCheckTimeout)

	var err error
	switch kind {
	case ConnectionAssistant:
		_, err = claude.ListModels(ctx, claude.Options{BaseURL: baseURL, APIKey: apiKey, HTTP: httpClient})
	case ConnectionCursor:
		_, err = cursor.ListModels(ctx, cursor.Options{APIKey: apiKey, HTTP: httpClient})
	case ConnectionServiceNow:
		err = c.checkServiceNowToken(ctx, apiKey, httpClient)
	default:
		return fmt.Errorf("%w: unknown connection %q", ErrInvalidConnection, kind)
	}

	return keyCheckResult(err)
}

// checkServiceNowToken reads one case through the connector with the new
// client token. Without the API key from the adapter environment the
// connector cannot be asked, so the token is stored as before. The same holds
// without an instance URL: there is nowhere to ask.
func (c *Connections) checkServiceNowToken(ctx context.Context, token string, httpClient *http.Client) error {
	creds := ServiceNowCredentials{
		URL:    firstNonEmpty(c.opts.EnvServiceNowURL),
		APIKey: firstNonEmpty(c.opts.EnvServiceNowAPIKey),
	}
	if creds.URL == "" || creds.APIKey == "" {
		return nil
	}

	client, err := servicenow.New(servicenow.Options{
		BaseURL:     creds.URL,
		APIKey:      creds.APIKey,
		ClientToken: token,
		HTTP:        httpClient,
		Timeout:     keyCheckTimeout,
		MaxAttempts: 1,
	})
	if err != nil {
		return err
	}

	_, err = client.ListTickets(ctx, 1, 0)

	return err
}

func keyCheckResult(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, claude.ErrUnauthorized),
		errors.Is(err, cursor.ErrUnauthorized),
		errors.Is(err, servicenow.ErrUnauthorized):
		return fmt.Errorf("%w: %v", ErrKeyRefused, err)
	default:
		return fmt.Errorf("%w: %v", ErrKeyUnchecked, err)
	}
}
