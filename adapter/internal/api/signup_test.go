package api_test

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
	"github.com/percona/pds/adapter/internal/testutil"
)

// signupFixture is a deployment that serves every Organization, against a real
// PostgreSQL schema of its own and a fake issuer.
type signupFixture struct {
	oidcFixture
	db *store.Store
	// mail and events collect what invitations send and record.
	mail   *fakeInviteMailer
	events *fakeInviteEvents
}

// newSignupFixture opens signup unless a test says otherwise through policy.
func newSignupFixture(t *testing.T, policy service.Signup) signupFixture {
	t.Helper()

	base := strings.TrimSpace(os.Getenv("ADAPTER_TEST_DATABASE_URL"))
	if base == "" {
		t.Skip("set ADAPTER_TEST_DATABASE_URL to run signup against a live PostgreSQL")
	}

	schema := fmt.Sprintf("signup_%d", time.Now().UnixNano())
	admin, err := store.Open(t.Context(), store.Options{URL: base})
	require.NoError(t, err)
	require.NoError(t, admin.Gorm().WithContext(t.Context()).Exec("CREATE SCHEMA "+schema).Error)
	t.Cleanup(func() {
		_ = admin.Gorm().Exec("DROP SCHEMA " + schema + " CASCADE").Error
		_ = admin.Close()
	})

	parsed, err := url.Parse(base)
	require.NoError(t, err)
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()

	db, err := store.Open(t.Context(), store.Options{URL: parsed.String(), Organization: "*"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Migrate(t.Context()))

	issuer := testutil.NewFakeIssuer(t)
	client, err := auth.NewOIDCClient(t.Context(), auth.OIDCConfig{
		Issuer: issuer.URL, ClientID: testutil.IssuerClientID, ClientSecret: testutil.IssuerClientSecret,
	})
	require.NoError(t, err)

	mail, events := &fakeInviteMailer{}, &fakeInviteEvents{}
	invitations := service.NewInvitations(db.Identity(),
		service.WithInvitationLinks(testConfig().HubPublicURL),
		service.WithInvitationMailer(mail, "PDS <invites@example.com>"),
		service.WithInvitationRecorder(events),
		service.WithInvitationPasswordCost(bcrypt.MinCost),
	)
	identity := service.NewIdentity(db.Identity(),
		service.WithPasswordCost(bcrypt.MinCost),
		service.WithSignup(policy),
		service.WithInvitationClaims(invitations),
	)
	srv := newInviteServer(identity, db, client, invitations, events)

	return signupFixture{oidcFixture: oidcFixture{srv: srv, issuer: issuer}, db: db, mail: mail, events: events}
}

func newInviteServer(
	identity *service.Identity,
	db *store.Store,
	client *auth.OIDCClient,
	invitations *service.Invitations,
	events service.ActivityRecorder,
) *api.Server {
	return api.NewServer(api.Deps{
		Config:      testConfig(),
		Logger:      log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
		Auth:        api.NewSessionAuthenticator(identity, ""),
		Identity:    identity,
		Directory:   service.NewDirectory(db.Identity(), events),
		Invitations: invitations,
		OIDC:        client,
		Databases:   service.NewDatabase(engine.NewRegistry()),
	})
}

var openSignup = service.Signup{Enabled: true, OrganizationCeiling: 100}

// signUpAs signs the fake issuer's next identity in and returns the callback.
func (f signupFixture) signUpAs(t *testing.T, subject, email, name string, verified bool) *httptest.ResponseRecorder {
	t.Helper()

	f.issuer.Identity = testutil.IssuerIdentity{Subject: subject, Email: email, EmailVerified: verified, Name: name}

	return f.signInThroughIssuer(t)
}

func (f signupFixture) sessionBody(t *testing.T, session *http.Cookie) map[string]any {
	t.Helper()

	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, withCookie(httptest.NewRequest(http.MethodGet, "/api/auth/session", nil), session))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	return decodeBody(t, rec)
}

func (f signupFixture) post(t *testing.T, session *http.Cookie, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := withCookie(httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)), session)
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)

	return rec
}

func TestFirstSignInCreatesTheOrganizationTeamAndAdmin(t *testing.T) {
	f := newSignupFixture(t, openSignup)

	rec := f.signUpAs(t, "auth0|ada", "ada@example.com", "Ada Lovelace", true)

	require.Equal(t, http.StatusFound, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, "/app/", rec.Header().Get("Location"))
	session := cookieNamed(rec, auth.SessionCookieName)
	require.NotNil(t, session)
	body := f.sessionBody(t, session)
	assert.Equal(t, "Ada Lovelace", body["user"])
	assert.Equal(t, "Ada Lovelace", body["organization"], "the Organization is named from the token")
	assert.Equal(t, "default", body["team"])
	assert.Equal(t, "admin", body["role"])
}

func TestSignUpNamesTheOrganizationFromTheEmailWhenTheTokenHasNoName(t *testing.T) {
	f := newSignupFixture(t, openSignup)

	rec := f.signUpAs(t, "auth0|ada", "ada@example.com", "", true)

	session := cookieNamed(rec, auth.SessionCookieName)
	require.NotNil(t, session)
	assert.Equal(t, "ada", f.sessionBody(t, session)["organization"])
}

func TestSecondSignInFindsTheSameOrganization(t *testing.T) {
	f := newSignupFixture(t, openSignup)
	f.signUpAs(t, "auth0|ada", "ada@example.com", "Ada", true)

	rec := f.signUpAs(t, "auth0|ada", "ada@example.com", "Ada", true)

	session := cookieNamed(rec, auth.SessionCookieName)
	require.NotNil(t, session)
	assert.Equal(t, "Ada", f.sessionBody(t, session)["organization"])
	assert.Len(t, f.sessionBody(t, session)["organizations"], 1, "a returning identity adds nothing")
}

func TestSignUpGatesRefuseWithASafeCodeAndCreateNothing(t *testing.T) {
	tests := []struct {
		name     string
		policy   service.Signup
		email    string
		verified bool
		code     string
	}{
		{"signup is off", service.Signup{}, "ada@example.com", true, "no_membership"},
		{"the email is not verified", openSignup, "ada@example.com", false, "email_unverified"},
		{"the token has no email", openSignup, "", true, "email_unverified"},
		{"the deployment is full", service.Signup{Enabled: true, OrganizationCeiling: 1}, "ada@example.com", true, "signup_unavailable"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newSignupFixture(t, tc.policy)
			if tc.policy.OrganizationCeiling == 1 {
				first := f.signUpAs(t, "auth0|first", "first@example.com", "First", true)
				require.NotNil(t, cookieNamed(first, auth.SessionCookieName))
			}

			rec := f.signUpAs(t, "auth0|ada", tc.email, "Ada", tc.verified)

			require.Equal(t, http.StatusFound, rec.Code)
			assert.Equal(t, "/app/login?error="+tc.code, rec.Header().Get("Location"))
			assert.Nil(t, cookieNamed(rec, auth.SessionCookieName))
			_, err := f.db.Identity().UserPlacement(t.Context(), f.issuer.URL, "auth0|ada")
			assert.ErrorIs(t, err, store.ErrNoMembership)
		})
	}
}

func TestSignUpIsRateLimitedPerAddress(t *testing.T) {
	f := newSignupFixture(t, openSignup)

	var last *httptest.ResponseRecorder
	for n := range 6 {
		last = f.signUpAs(t, fmt.Sprintf("auth0|person%d", n), fmt.Sprintf("p%d@example.com", n), "P", true)
	}

	assert.Equal(t, "/app/login?error=signup_rate_limited", last.Header().Get("Location"))
}

func TestSignInOfAKnownIdentityIsNotRateLimited(t *testing.T) {
	f := newSignupFixture(t, openSignup)
	f.signUpAs(t, "auth0|ada", "ada@example.com", "Ada", true)

	for range 8 {
		rec := f.signUpAs(t, "auth0|ada", "ada@example.com", "Ada", true)
		require.NotNil(t, cookieNamed(rec, auth.SessionCookieName))
	}
}

func TestAPersonCreatesAnotherOrganizationUpToTheirCap(t *testing.T) {
	f := newSignupFixture(t, openSignup)
	session := cookieNamed(f.signUpAs(t, "auth0|ada", "ada@example.com", "Ada", true), auth.SessionCookieName)
	require.NotNil(t, session)

	for _, name := range []string{"Second", "Third"} {
		rec := f.post(t, session, "/api/auth/organizations", `{"name":"`+name+`"}`)
		require.Equal(t, http.StatusNoContent, rec.Code, "body: %s", rec.Body.String())
		assert.Equal(t, name, f.sessionBody(t, session)["organization"], "the session moves into the new Organization")
	}

	rec := f.post(t, session, "/api/auth/organizations", `{"name":"Fourth"}`)

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, api.CodeConflict, decodeBody(t, rec)["code"])
	assert.Equal(t, "Third", f.sessionBody(t, session)["organization"])
}

func TestCreatingAnOrganizationNeedsANameOfUpTo200Characters(t *testing.T) {
	f := newSignupFixture(t, openSignup)
	session := cookieNamed(f.signUpAs(t, "auth0|ada", "ada@example.com", "Ada", true), auth.SessionCookieName)

	for _, name := range []string{"   ", strings.Repeat("x", 201)} {
		rec := f.post(t, session, "/api/auth/organizations", `{"name":"`+name+`"}`)

		assert.Equal(t, http.StatusBadRequest, rec.Code, "name %q", name)
	}
}

func TestCreatingOrSwitchingOrganizationsNeedsASession(t *testing.T) {
	f := newSignupFixture(t, openSignup)

	for _, path := range []string{"/api/auth/organizations", "/api/auth/organization"} {
		rec := httptest.NewRecorder()
		f.srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"name":"X","organizationId":1}`)))

		assert.Equal(t, http.StatusUnauthorized, rec.Code, path)
	}
}

func TestSwitchingOrganizationMovesTheSessionAndTheNextSignIn(t *testing.T) {
	f := newSignupFixture(t, openSignup)
	session := cookieNamed(f.signUpAs(t, "auth0|ada", "ada@example.com", "Ada", true), auth.SessionCookieName)
	require.Equal(t, http.StatusNoContent, f.post(t, session, "/api/auth/organizations", `{"name":"Second"}`).Code)

	organizations, ok := f.sessionBody(t, session)["organizations"].([]any)
	require.True(t, ok)
	require.Len(t, organizations, 2)
	first, ok := organizations[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Ada", first["name"])

	rec := f.post(t, session, "/api/auth/organization", fmt.Sprintf(`{"organizationId":%v}`, first["id"]))

	require.Equal(t, http.StatusNoContent, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, "Ada", f.sessionBody(t, session)["organization"])

	again := f.signUpAs(t, "auth0|ada", "ada@example.com", "Ada", true)
	next := cookieNamed(again, auth.SessionCookieName)
	require.NotNil(t, next)
	assert.Equal(t, "Ada", f.sessionBody(t, next)["organization"], "the next sign in lands in the last used")
}

func TestSwitchingToAnOrganizationThePersonDoesNotBelongToIsNotFound(t *testing.T) {
	f := newSignupFixture(t, openSignup)
	session := cookieNamed(f.signUpAs(t, "auth0|ada", "ada@example.com", "Ada", true), auth.SessionCookieName)
	other := cookieNamed(f.signUpAs(t, "auth0|grace", "grace@example.com", "Grace", true), auth.SessionCookieName)
	theirs := f.sessionBody(t, other)["organizations"].([]any)[0].(map[string]any)

	rec := f.post(t, session, "/api/auth/organization", fmt.Sprintf(`{"organizationId":%v}`, theirs["id"]))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "Ada", f.sessionBody(t, session)["organization"])
}

func TestAPasswordSessionCannotCreateOrSwitchOrganizations(t *testing.T) {
	srv, _, _ := newLoginServer(t)
	session := signIn(t, srv, loginName, loginPassword)

	for _, path := range []string{"/api/auth/organizations", "/api/auth/organization"} {
		req := withCookie(httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"name":"X","organizationId":1}`)), session)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusForbidden, rec.Code, path)
	}
}

func TestSignUpNamesTheOrganizationWhatThePersonTypedOnTheSignInPage(t *testing.T) {
	f := newSignupFixture(t, openSignup)
	f.issuer.Identity = testutil.IssuerIdentity{
		Subject: "auth0|ada", Email: "ada@example.com", EmailVerified: true, Name: "Ada Lovelace",
	}

	rec := f.signInThrough(t, "/api/auth/oidc/start?organization="+url.QueryEscape("  Analytical Engines "))

	session := cookieNamed(rec, auth.SessionCookieName)
	require.NotNil(t, session, "body: %s", rec.Body.String())
	assert.Equal(t, "Analytical Engines", f.sessionBody(t, session)["organization"])
}

func TestAnOrganizationNameIsIgnoredForSomebodyWhoAlreadyHasOne(t *testing.T) {
	f := newSignupFixture(t, openSignup)
	f.signUpAs(t, "auth0|ada", "ada@example.com", "Ada", true)

	rec := f.signInThrough(t, "/api/auth/oidc/start?organization=Another")

	session := cookieNamed(rec, auth.SessionCookieName)
	require.NotNil(t, session)
	assert.Equal(t, "Ada", f.sessionBody(t, session)["organization"])
	assert.Len(t, f.sessionBody(t, session)["organizations"], 1)
}
