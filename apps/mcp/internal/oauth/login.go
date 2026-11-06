package oauth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var (
	// ErrDenied means the person did not allow the sign in.
	ErrDenied = errors.New("the sign in was declined")
	// ErrLoginTimedOut means nobody finished the sign in in the browser in time.
	ErrLoginTimedOut = errors.New("the sign in was not finished in time")
	// ErrLoginRefused means the control plane answered the sign in with an error or with a
	// response that did not come from it.
	ErrLoginRefused = errors.New("the control plane refused the sign in")
)

const defaultLoginTimeout = 5 * time.Minute

// LoginOptions is one run of the authorization code flow.
type LoginOptions struct {
	Server Server
	HTTP   *http.Client
	// Scopes are asked for as they are. The person can allow less on the consent screen.
	Scopes []string
	// Open sends the person's browser to the address. It may be nil.
	Open func(address string) error
	// Announce is told the address before Open is called, so it can be shown to a person whose
	// browser did not open.
	Announce func(address string)
	// Timeout is how long to wait for the browser. It defaults to five minutes.
	Timeout time.Duration
}

type callback struct {
	code string
	err  error
}

// Login runs the authorization code flow with S256 PKCE as a native client: a listener
// on 127.0.0.1 and an ephemeral port receives the response, takes one answer that carries the
// right state, and is closed before the code is exchanged. The response must name the control
// plane as its issuer. Nothing is written to disk.
func Login(ctx context.Context, opts LoginOptions) (Tokens, error) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultLoginTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", "127.0.0.1:0")
	if err != nil {
		return Tokens{}, fmt.Errorf("listen for the sign in on this computer: %w", err)
	}
	redirect := "http://" + listener.Addr().String() + "/callback"

	verifier, state := NewVerifier(), randomString(24)
	answers := make(chan callback, 1)
	srv := &http.Server{
		Handler:           callbackHandler(listener.Addr().String(), state, opts.Server.Issuer, answers),
		ReadHeaderTimeout: 10 * time.Second,
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = srv.Serve(listener)
	}()
	// The listener is closed as soon as the flow ends, however it ends.
	defer func() {
		shutdown, stop := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer stop()
		_ = srv.Shutdown(shutdown)
		<-served
	}()

	address := authorizationURL(opts, redirect, state, Challenge(verifier))
	if opts.Announce != nil {
		opts.Announce(address)
	}
	if opts.Open != nil {
		// A browser that cannot be opened is not fatal: the address was announced.
		_ = opts.Open(address)
	}

	var answer callback
	select {
	case answer = <-answers:
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Tokens{}, ErrLoginTimedOut
		}

		return Tokens{}, ctx.Err()
	}
	if answer.err != nil {
		return Tokens{}, answer.err
	}

	tokens, err := exchange(ctx, opts.HTTP, opts.Server, url.Values{
		"grant_type": {"authorization_code"}, "code": {answer.code}, "redirect_uri": {redirect},
		"code_verifier": {verifier},
	}, time.Now())
	if err != nil {
		return Tokens{}, err
	}
	if tokens.RefreshToken == "" {
		return Tokens{}, ErrNoRefreshToken
	}

	return tokens, nil
}

func authorizationURL(opts LoginOptions, redirect, state, challenge string) string {
	query := url.Values{
		"response_type": {"code"}, "client_id": {LocalClientID}, "redirect_uri": {redirect},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {state},
		"scope": {strings.Join(opts.Scopes, " ")}, "resource": {opts.Server.Resource},
	}

	return opts.Server.AuthorizationEndpoint + "?" + query.Encode()
}

// callbackHandler answers the browser's return. A request with the wrong state, the wrong host or
// the wrong method is refused and leaves the flow waiting; the first one with the right state ends
// it, so a second callback finds nothing listening.
func callbackHandler(host, state, issuer string, answers chan<- callback) http.Handler {
	var once sync.Once

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")

		// The Host check keeps a web page that rebinds a name to 127.0.0.1 from reaching this.
		if r.URL.Path != "/callback" || r.Method != http.MethodGet || r.Host != host {
			page(w, http.StatusNotFound, "Not found.")

			return
		}
		query := r.URL.Query()
		if subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(state)) != 1 {
			page(w, http.StatusBadRequest, "This sign in response does not match the request that is waiting. Go back to the terminal and try again.")

			return
		}

		result := callback{code: query.Get("code")}
		message := "pds-mcp is signed in. You can close this tab."
		switch {
		case query.Get("error") == "access_denied":
			result = callback{err: ErrDenied}
			message = "The sign in was declined. You can close this tab."
		case query.Get("error") != "" || result.code == "":
			result = callback{err: ErrLoginRefused}
			message = "The sign in did not finish. Go back to the terminal."
		case subtle.ConstantTimeCompare([]byte(query.Get("iss")), []byte(issuer)) != 1:
			// A response that does not name this control plane did not come from it.
			result = callback{err: ErrLoginRefused}
			message = "The sign in did not finish. Go back to the terminal."
		}

		status := http.StatusOK
		if result.err != nil {
			status = http.StatusBadRequest
		}
		page(w, status, message)
		once.Do(func() { answers <- result })
	})
}

func page(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>pds-mcp</title></head>`+
		`<body style="font:16px system-ui,sans-serif;max-width:32rem;margin:15vh auto;padding:0 1rem"><p>%s</p></body></html>`,
		html.EscapeString(message))
}
