package api_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
	"github.com/percona/pds/adapter/internal/testutil"
)

type fakeInviteMailer struct {
	mu   sync.Mutex
	sent []service.InvitationEmail
	err  error
}

func (m *fakeInviteMailer) Send(_ context.Context, email service.InvitationEmail) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.sent = append(m.sent, email)

	return nil
}

func (m *fakeInviteMailer) emails() []service.InvitationEmail {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]service.InvitationEmail(nil), m.sent...)
}

type fakeInviteEvents struct {
	mu     sync.Mutex
	events []service.ActivityEvent
	// actors is the User each event was recorded for, from its context.
	actors []uint
}

func (e *fakeInviteEvents) Record(ctx context.Context, event service.ActivityEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event)
	scope, _ := auth.ScopeFromContext(ctx)
	e.actors = append(e.actors, scope.UserID)
}

func (e *fakeInviteEvents) actions() []string {
	e.mu.Lock()
	defer e.mu.Unlock()

	var out []string
	for _, event := range e.events {
		out = append(out, event.Action+":"+event.Outcome)
	}

	return out
}

func (e *fakeInviteEvents) targets() []string {
	e.mu.Lock()
	defer e.mu.Unlock()

	var out []string
	for _, event := range e.events {
		out = append(out, event.Target)
	}

	return out
}

// invite is what creating or resending an invitation answers, with the token
// taken out of the link.
type invite struct {
	ID    float64
	Token string
	Body  map[string]any
}

func (f directoryFixture) invite(t *testing.T, body string) (invite, *httptest.ResponseRecorder) {
	t.Helper()

	rec := f.call(t, f.admin, http.MethodPost, "/api/invitations", body)
	if rec.Code != http.StatusCreated {
		return invite{}, rec
	}

	return readInvite(t, rec), rec
}

func readInvite(t *testing.T, rec *httptest.ResponseRecorder) invite {
	t.Helper()

	body := decodeBody(t, rec)
	link, _ := body["link"].(string)
	require.NotEmpty(t, link, "body: %s", rec.Body.String())
	parsed, err := url.Parse(link)
	require.NoError(t, err)
	assert.Equal(t, "https://hub.example/claim", parsed.Scheme+"://"+parsed.Host+parsed.Path)

	return invite{ID: body["id"].(float64), Token: parsed.Query().Get("token"), Body: body}
}

func (f directoryFixture) mustInvite(t *testing.T, body string) invite {
	t.Helper()

	in, rec := f.invite(t, body)
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

	return in
}

// claimAs signs the fake issuer's next identity in through an invitation link.
func (f directoryFixture) claimAs(
	t *testing.T, token, subject, email string, verified bool,
) *httptest.ResponseRecorder {
	t.Helper()

	f.issuer.Identity = testutil.IssuerIdentity{Subject: subject, Email: email, EmailVerified: verified, Name: subject}

	return f.signInThrough(t, "/api/auth/oidc/start?invite="+url.QueryEscape(token))
}

func redirectQuery(t *testing.T, rec *httptest.ResponseRecorder) url.Values {
	t.Helper()

	require.Equal(t, http.StatusFound, rec.Code, "body: %s", rec.Body.String())
	location, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)

	return location.Query()
}

func (f directoryFixture) pendingInvitations(t *testing.T) []any {
	t.Helper()

	rec := f.call(t, f.admin, http.MethodGet, "/api/invitations", "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	return decodeBody(t, rec)["invitations"].([]any)
}

func TestAnAdminInvitesAUserAndSeesItPending(t *testing.T) {
	f := newDirectoryFixture(t)

	in, rec := f.invite(t, `{"email":" Bob@Example.com ","team":"default","role":"user"}`)

	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, "bob@example.com", in.Body["email"], "the address is trimmed and lowercased")
	assert.Equal(t, "default", in.Body["team"])
	assert.Equal(t, "user", in.Body["role"])
	assert.Equal(t, true, in.Body["emailed"])

	pending := f.pendingInvitations(t)
	require.Len(t, pending, 1)
	row := pending[0].(map[string]any)
	assert.Equal(t, "bob@example.com", row["email"])
	assert.NotContains(t, row, "link", "the link is shown once, at creation and after a resend")
	assert.NotContains(t, fmt.Sprint(row), in.Token)
}

func TestInvitationRoutesAreForAdminsOnly(t *testing.T) {
	f := newDirectoryFixture(t)
	f.addUser(t, "auth0|bob", store.RoleUser)
	session := f.signInAs(t, "auth0|bob")

	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/api/invitations", ""},
		{http.MethodPost, "/api/invitations", `{"email":"x@example.com","team":"default","role":"user"}`},
		{http.MethodPost, "/api/invitations/1/resend", ""},
		{http.MethodDelete, "/api/invitations/1", ""},
	}
	for _, route := range routes {
		rec := f.call(t, session, route.method, route.path, route.body)

		assert.Equal(t, http.StatusForbidden, rec.Code, "%s %s", route.method, route.path)
	}
}

func TestAnInvitationNeedsAValidAddressAKnownTeamAndARole(t *testing.T) {
	f := newDirectoryFixture(t)

	for body, want := range map[string]int{
		`{"email":"not-an-address","team":"default","role":"user"}`:   http.StatusBadRequest,
		`{"email":"","team":"default","role":"user"}`:                 http.StatusBadRequest,
		`{"email":"a@example.com","team":"default","role":"owner"}`:   http.StatusBadRequest,
		`{"email":"a@example.com","team":"nowhere","role":"user"}`:    http.StatusNotFound,
		`{"email":"a@example.com","team":"","role":"user"}`:           http.StatusBadRequest,
		`{"email":"a@example.com","team":"default","role":"admin"}`:   http.StatusCreated,
		`{"email":"ada@example.com","team":"default","role":"admin"}`: http.StatusConflict,
	} {
		rec := f.call(t, f.admin, http.MethodPost, "/api/invitations", body)

		assert.Equal(t, want, rec.Code, "%s: %s", body, rec.Body.String())
	}
}

func TestOnlyOnePendingInvitationPerEmailAndRevokingFreesIt(t *testing.T) {
	f := newDirectoryFixture(t)
	first := f.mustInvite(t, `{"email":"bob@example.com","team":"default","role":"user"}`)

	_, rec := f.invite(t, `{"email":"BOB@example.com","team":"default","role":"user"}`)
	assert.Equal(t, http.StatusConflict, rec.Code)

	revoke := f.call(t, f.admin, http.MethodDelete, fmt.Sprintf("/api/invitations/%.0f", first.ID), "")
	require.Equal(t, http.StatusNoContent, revoke.Code)
	assert.Empty(t, f.pendingInvitations(t))

	f.mustInvite(t, `{"email":"bob@example.com","team":"default","role":"user"}`)
}

func TestAnInvitationClaimedThroughTheIssuerAddsAMembership(t *testing.T) {
	f := newDirectoryFixture(t)
	_, err := f.db.Identity().CreateTeam(t.Context(), f.org, "payments", time.Now())
	require.NoError(t, err)
	in := f.mustInvite(t, `{"email":"bob@example.com","team":"payments","role":"admin"}`)

	rec := f.claimAs(t, in.Token, "auth0|bob", "Bob@example.com", true)

	require.Equal(t, http.StatusFound, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, "/app/", rec.Header().Get("Location"))
	session := cookieNamed(rec, auth.SessionCookieName)
	require.NotNil(t, session)
	body := f.sessionBody(t, session)
	assert.Equal(t, "payments", body["team"])
	assert.Equal(t, "admin", body["role"])
	assert.Equal(t, "Ada", body["organization"])
	assert.Empty(t, f.pendingInvitations(t), "a claimed invitation is no longer pending")
	assert.Contains(t, f.events.actions(), "invitation.claim:succeeded")
}

func TestAClaimedLinkCannotBeUsedAgain(t *testing.T) {
	f := newDirectoryFixture(t)
	in := f.mustInvite(t, `{"email":"bob@example.com","team":"default","role":"user"}`)
	require.Equal(t, http.StatusFound, f.claimAs(t, in.Token, "auth0|bob", "bob@example.com", true).Code)

	rec := f.claimAs(t, in.Token, "auth0|bob2", "bob@example.com", true)

	assert.Equal(t, "invite_spent", redirectQuery(t, rec).Get("error"))
	assert.Nil(t, cookieNamed(rec, auth.SessionCookieName))
}

func TestAMismatchedEmailIsRefusedAndTheInvitationStaysUnspent(t *testing.T) {
	f := newDirectoryFixture(t)
	in := f.mustInvite(t, `{"email":"bob@example.com","team":"default","role":"user"}`)

	rec := f.claimAs(t, in.Token, "auth0|eve", "eve@example.com", true)

	query := redirectQuery(t, rec)
	assert.Equal(t, "invite_email_mismatch", query.Get("error"))
	assert.Equal(t, "b***@example.com", query.Get("invited"), "the invited address is shown masked")
	assert.Nil(t, cookieNamed(rec, auth.SessionCookieName), "a refused claim opens no session")
	assert.Len(t, f.pendingInvitations(t), 1, "the invitation stays unspent")
	assert.Contains(t, f.events.actions(), "invitation.refuse:failed")

	again := f.claimAs(t, in.Token, "auth0|bob", "bob@example.com", true)
	assert.Equal(t, "/app/", again.Header().Get("Location"), "the right account can still claim it")
}

func TestAnUnverifiedOrMissingEmailIsRefusedAndTheInvitationStaysUnspent(t *testing.T) {
	f := newDirectoryFixture(t)
	in := f.mustInvite(t, `{"email":"bob@example.com","team":"default","role":"user"}`)

	for name, claim := range map[string]struct {
		email    string
		verified bool
	}{
		"unverified": {"bob@example.com", false},
		"missing":    {"", true},
	} {
		rec := f.claimAs(t, in.Token, "auth0|bob-"+name, claim.email, claim.verified)

		query := redirectQuery(t, rec)
		assert.Equal(t, "invite_email_unverified", query.Get("error"), name)
		assert.Equal(t, "b***@example.com", query.Get("invited"), name)
		assert.Nil(t, cookieNamed(rec, auth.SessionCookieName), name)
	}
	assert.Len(t, f.pendingInvitations(t), 1)
}

func TestResendInvalidatesTheOldLinkAndTheNewOneWorks(t *testing.T) {
	f := newDirectoryFixture(t)
	old := f.mustInvite(t, `{"email":"bob@example.com","team":"default","role":"user"}`)

	rec := f.call(t, f.admin, http.MethodPost, fmt.Sprintf("/api/invitations/%.0f/resend", old.ID), "")
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	fresh := readInvite(t, rec)

	require.NotEqual(t, old.Token, fresh.Token)
	assert.Len(t, f.pendingInvitations(t), 1, "there is still one pending invitation for the address")
	stale := f.claimAs(t, old.Token, "auth0|bob", "bob@example.com", true)
	assert.Equal(t, "invite_invalid", redirectQuery(t, stale).Get("error"))
	good := f.claimAs(t, fresh.Token, "auth0|bob", "bob@example.com", true)
	assert.Equal(t, "/app/", good.Header().Get("Location"))
	assert.Contains(t, f.events.actions(), "invitation.resend:succeeded")
}

func TestARevokedLinkIsRefused(t *testing.T) {
	f := newDirectoryFixture(t)
	in := f.mustInvite(t, `{"email":"bob@example.com","team":"default","role":"user"}`)
	require.Equal(t, http.StatusNoContent,
		f.call(t, f.admin, http.MethodDelete, fmt.Sprintf("/api/invitations/%.0f", in.ID), "").Code)

	rec := f.claimAs(t, in.Token, "auth0|bob", "bob@example.com", true)

	assert.Equal(t, "invite_invalid", redirectQuery(t, rec).Get("error"))
	assert.Contains(t, f.events.actions(), "invitation.revoke:succeeded")
}

func TestAnExpiredLinkIsRefused(t *testing.T) {
	f := newDirectoryFixture(t)
	in := f.mustInvite(t, `{"email":"bob@example.com","team":"default","role":"user"}`)
	require.NoError(t, f.db.Gorm().Model(&store.Invitation{}).
		Where("id = ?", uint(in.ID)).Update("expires_at", time.Now().Add(-time.Minute)).Error)

	rec := f.claimAs(t, in.Token, "auth0|bob", "bob@example.com", true)

	assert.Equal(t, "invite_expired", redirectQuery(t, rec).Get("error"))
	assert.Empty(t, f.pendingInvitations(t), "an expired invitation is not pending")
	_, again := f.invite(t, `{"email":"bob@example.com","team":"default","role":"user"}`)
	assert.Equal(t, http.StatusCreated, again.Code, "an expired invitation does not block a new one")
}

func TestAnInvitationLivesSevenDaysByDefault(t *testing.T) {
	f := newDirectoryFixture(t)

	in := f.mustInvite(t, `{"email":"bob@example.com","team":"default","role":"user"}`)

	expires, err := time.Parse(time.RFC3339, in.Body["expiresAt"].(string))
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(7*24*time.Hour), expires, time.Minute)
}

func TestAnAnyEmailInvitationIsUserOnlyAndLivesFortyEightHours(t *testing.T) {
	f := newDirectoryFixture(t)

	_, admin := f.invite(t, `{"anyEmail":true,"team":"default","role":"admin"}`)
	assert.Equal(t, http.StatusBadRequest, admin.Code, "an any-email invitation may not carry the admin role")

	in := f.mustInvite(t, `{"anyEmail":true,"team":"default","role":"user"}`)
	expires, err := time.Parse(time.RFC3339, in.Body["expiresAt"].(string))
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(48*time.Hour), expires, time.Minute)
	assert.Empty(t, f.mail.emails(), "there is no address to send it to")

	rec := f.claimAs(t, in.Token, "auth0|anyone", "anyone@elsewhere.com", true)
	assert.Equal(t, "/app/", rec.Header().Get("Location"), "any verified address may claim it")

	unverified := f.mustInvite(t, `{"anyEmail":true,"team":"default","role":"user"}`)
	refused := f.claimAs(t, unverified.Token, "auth0|nobody", "nobody@elsewhere.com", false)
	assert.Equal(t, "invite_email_unverified", redirectQuery(t, refused).Get("error"),
		"even an any-email claim needs a verified address")
	assert.Empty(t, redirectQuery(t, refused).Get("invited"), "there is no address to show")
}

func TestAPendingInvitationForTheVerifiedEmailWinsOverSigningUp(t *testing.T) {
	f := newDirectoryFixture(t)
	f.mustInvite(t, `{"email":"bob@example.com","team":"default","role":"user"}`)

	rec := f.signUpAs(t, "auth0|bob", "bob@example.com", "Bob", true)

	require.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "/app/", rec.Header().Get("Location"))
	body := f.sessionBody(t, cookieNamed(rec, auth.SessionCookieName))
	assert.Equal(t, "Ada", body["organization"], "Bob lands in the inviting Organization, not a new one")
	assert.Equal(t, "user", body["role"])
	assert.Empty(t, f.pendingInvitations(t))
}

func TestAnUnverifiedEmailDoesNotClaimAPendingInvitationBySigningUp(t *testing.T) {
	f := newDirectoryFixture(t)
	f.mustInvite(t, `{"email":"bob@example.com","team":"default","role":"user"}`)

	rec := f.signUpAs(t, "auth0|bob", "bob@example.com", "Bob", false)

	assert.Equal(t, "email_unverified", redirectQuery(t, rec).Get("error"))
	assert.Len(t, f.pendingInvitations(t), 1)
}

func TestAMemberCannotClaimAnInvitationToTheirOwnOrganization(t *testing.T) {
	f := newDirectoryFixture(t)
	f.addUser(t, "auth0|bob", store.RoleUser)
	in := f.mustInvite(t, `{"anyEmail":true,"team":"default","role":"user"}`)

	rec := f.claimAs(t, in.Token, "auth0|bob", "bob@example.com", true)

	assert.Equal(t, "invite_already_member", redirectQuery(t, rec).Get("error"))
	assert.Len(t, f.pendingInvitations(t), 1, "the invitation stays unspent")
}

func TestAnInvitationIsEmailedWhenAMailerIsConfigured(t *testing.T) {
	f := newDirectoryFixture(t)

	in := f.mustInvite(t, `{"email":"bob@example.com","team":"default","role":"user"}`)

	assert.Equal(t, true, in.Body["emailed"])
	sent := f.mail.emails()
	require.Len(t, sent, 1)
	assert.Equal(t, []string{"bob@example.com"}, sent[0].To)
	assert.Equal(t, "PDS <invites@example.com>", sent[0].From)
	assert.Contains(t, sent[0].Text, "token="+in.Token)
}

func TestAFailedEmailStillCreatesTheInvitation(t *testing.T) {
	f := newDirectoryFixture(t)
	f.mail.err = fmt.Errorf("resend is down")

	in := f.mustInvite(t, `{"email":"bob@example.com","team":"default","role":"user"}`)

	assert.Equal(t, false, in.Body["emailed"], "the admin is told the email did not go, and still has the link")
	assert.Len(t, f.pendingInvitations(t), 1)
}

func TestResendEmailsTheNewLink(t *testing.T) {
	f := newDirectoryFixture(t)
	old := f.mustInvite(t, `{"email":"bob@example.com","team":"default","role":"user"}`)

	rec := f.call(t, f.admin, http.MethodPost, fmt.Sprintf("/api/invitations/%.0f/resend", old.ID), "")
	fresh := readInvite(t, rec)

	sent := f.mail.emails()
	require.Len(t, sent, 2)
	assert.Contains(t, sent[1].Text, "token="+fresh.Token)
	assert.NotContains(t, sent[1].Text, old.Token)
}

func TestInvitationsOfAnotherOrganizationAreNotFound(t *testing.T) {
	f := newDirectoryFixture(t)
	other := cookieNamed(f.signUpAs(t, "auth0|eve", "eve@example.com", "Eve", true), auth.SessionCookieName)
	in := f.mustInvite(t, `{"email":"bob@example.com","team":"default","role":"user"}`)

	resend := f.call(t, other, http.MethodPost, fmt.Sprintf("/api/invitations/%.0f/resend", in.ID), "")
	revoke := f.call(t, other, http.MethodDelete, fmt.Sprintf("/api/invitations/%.0f", in.ID), "")
	list := f.call(t, other, http.MethodGet, "/api/invitations", "")

	assert.Equal(t, http.StatusNotFound, resend.Code)
	assert.Equal(t, http.StatusNotFound, revoke.Code)
	require.Equal(t, http.StatusOK, list.Code)
	assert.Empty(t, decodeBody(t, list)["invitations"])
	assert.Len(t, f.pendingInvitations(t), 1, "the other Organization changed nothing")
}

func TestThePreviewNamesWhatTheLinkIsForWithoutSpendingIt(t *testing.T) {
	f := newDirectoryFixture(t)
	in := f.mustInvite(t, `{"email":"bob@example.com","team":"default","role":"user"}`)

	for range 2 {
		rec := do(t, f.srv, http.MethodGet, "/api/auth/invite?token="+url.QueryEscape(in.Token), nil)

		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		body := decodeBody(t, rec)
		assert.Equal(t, "Ada", body["organization"])
		assert.Equal(t, "default", body["team"])
		assert.Equal(t, "user", body["role"])
		assert.Equal(t, "b***@example.com", body["email"])
		assert.NotContains(t, rec.Body.String(), "bob@example.com")
	}
}

func TestThePreviewOfALinkNobodyIssuedIsNotFound(t *testing.T) {
	f := newDirectoryFixture(t)

	rec := do(t, f.srv, http.MethodGet, "/api/auth/invite?token=nope", nil)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestInvitationActivityRecordsInvitedAndRevoked(t *testing.T) {
	f := newDirectoryFixture(t)

	in := f.mustInvite(t, `{"email":"bob@example.com","team":"default","role":"user"}`)
	f.call(t, f.admin, http.MethodDelete, fmt.Sprintf("/api/invitations/%.0f", in.ID), "")

	assert.Equal(t, []string{"invitation.create:succeeded", "invitation.revoke:succeeded"}, f.events.actions())
	for _, event := range f.events.events {
		assert.NotContains(t, event.Detail, in.Token, "a link never reaches the activity record")
		assert.NotContains(t, event.Target, in.Token)
	}
}

// withoutOIDC serves the same database with the password login alone: no
// issuer, so a claim sets a password.
func (f directoryFixture) withoutOIDC(t *testing.T) directoryFixture {
	t.Helper()

	invitations := service.NewInvitations(f.db.Identity(),
		service.WithInvitationLinks(testConfig().HubPublicURL),
		service.WithInvitationRecorder(f.events),
		service.WithInvitationPasswordCost(bcrypt.MinCost),
		service.WithInvitationPasswords(true),
	)
	identity := service.NewIdentity(f.db.Identity(), service.WithPasswordCost(bcrypt.MinCost))
	out := f
	out.srv = newInviteServer(identity, f.db, nil, invitations, f.events)

	return out
}

func TestAClaimWithTheIssuerOffSetsAPassword(t *testing.T) {
	f := newDirectoryFixture(t)
	in := f.mustInvite(t, `{"email":"bob@example.com","team":"default","role":"user"}`)
	p := f.withoutOIDC(t)

	preview := do(t, p.srv, http.MethodGet, "/api/auth/invite?token="+url.QueryEscape(in.Token), nil)
	require.Equal(t, http.StatusOK, preview.Code, "body: %s", preview.Body.String())

	claim := p.post(t, nil, "/api/auth/invite",
		fmt.Sprintf(`{"token":%q,"login":"bob","password":"correct horse battery"}`, in.Token))
	require.Equal(t, http.StatusOK, claim.Code, "body: %s", claim.Body.String())

	login := p.post(t, nil, "/api/auth/login", `{"username":"bob","password":"correct horse battery"}`)
	require.Equal(t, http.StatusOK, login.Code, "body: %s", login.Body.String())
	body := p.sessionBody(t, cookieNamed(login, auth.SessionCookieName))
	assert.Equal(t, "Ada", body["organization"])
	assert.Equal(t, "default", body["team"])
	assert.Equal(t, "user", body["role"])
	assert.Empty(t, f.pendingInvitations(t))
}

func TestAPasswordClaimIsSingleUseAndNeedsAFreeLogin(t *testing.T) {
	f := newDirectoryFixture(t)
	in := f.mustInvite(t, `{"email":"bob@example.com","team":"default","role":"user"}`)
	other := f.mustInvite(t, `{"email":"rob@example.com","team":"default","role":"user"}`)
	p := f.withoutOIDC(t)
	claim := func(token, login string) int {
		return p.post(t, nil, "/api/auth/invite",
			fmt.Sprintf(`{"token":%q,"login":%q,"password":"correct horse battery"}`, token, login)).Code
	}

	assert.Equal(t, http.StatusBadRequest, claim(in.Token, "Not A Login"))
	assert.Equal(t, http.StatusOK, claim(in.Token, "bob"))
	assert.Equal(t, http.StatusConflict, claim(in.Token, "bob2"), "a spent link is refused")
	assert.Equal(t, http.StatusConflict, claim(other.Token, "bob"), "a login somebody holds is refused")
	assert.Len(t, f.pendingInvitations(t), 1, "the refused claim left the other invitation unspent")
	assert.Equal(t, http.StatusOK, claim(other.Token, "rob"))
}

func TestAPasswordClaimIsRefusedWhileTheIssuerIsOn(t *testing.T) {
	f := newDirectoryFixture(t)
	in := f.mustInvite(t, `{"email":"bob@example.com","team":"default","role":"user"}`)

	rec := f.post(t, nil, "/api/auth/invite",
		fmt.Sprintf(`{"token":%q,"login":"bob","password":"correct horse battery"}`, in.Token))

	assert.Equal(t, http.StatusConflict, rec.Code,
		"with an issuer the email has to be verified, which a password cannot do")
	assert.Len(t, f.pendingInvitations(t), 1)
}

func TestAShortPasswordLeavesTheInvitationUnspent(t *testing.T) {
	f := newDirectoryFixture(t)
	in := f.mustInvite(t, `{"email":"bob@example.com","team":"default","role":"user"}`)
	p := f.withoutOIDC(t)

	rec := p.post(t, nil, "/api/auth/invite", fmt.Sprintf(`{"token":%q,"login":"bob","password":"short"}`, in.Token))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Len(t, f.pendingInvitations(t), 1)
	assert.True(t, strings.Contains(rec.Body.String(), "12 to 72"))
}
