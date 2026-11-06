package service_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
)

func newSignupService(t *testing.T, fake *fakeIdentityStore, login string, policy service.Signup) *service.Identity {
	t.Helper()

	return service.NewIdentity(fake,
		service.WithServedOrganization(login),
		service.WithIdentityClock(func() time.Time { return identityNow }),
		service.WithPasswordCost(bcrypt.MinCost),
		service.WithSignup(policy),
	)
}

func verified(subject, email, name string) auth.OIDCClaims {
	return auth.OIDCClaims{Subject: subject, Email: email, EmailVerified: true, Name: name}
}

func TestSignUpNeedsTheSwitchAndAnUnpinnedDeployment(t *testing.T) {
	tests := []struct {
		name   string
		login  string
		policy service.Signup
	}{
		{"signup is off", "", service.Signup{}},
		{"the deployment is pinned to one login", "acme", service.Signup{Enabled: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeIdentityStore()
			identity := newSignupService(t, fake, tc.login, tc.policy)

			_, err := identity.SignInOIDC(t.Context(), "https://login.example/", verified("s", "a@example.com", "Ada"), "192.0.2.1", "", "")

			assert.ErrorIs(t, err, store.ErrNoMembership)
			assert.Empty(t, fake.created)
		})
	}
}

func TestSignUpRefusesAnEmailTheIssuerDidNotVerify(t *testing.T) {
	fake := newFakeIdentityStore()
	identity := newSignupService(t, fake, "", service.Signup{Enabled: true})

	for _, claims := range []auth.OIDCClaims{
		{Subject: "s", Email: "a@example.com", EmailVerified: false},
		{Subject: "s", Email: "  ", EmailVerified: true},
	} {
		_, err := identity.SignInOIDC(t.Context(), "https://login.example/", claims, "192.0.2.1", "", "")

		assert.ErrorIs(t, err, service.ErrSignUpUnverified)
	}
	assert.Empty(t, fake.created)
}

func TestSignUpRateLimitsPerAddress(t *testing.T) {
	fake := newFakeIdentityStore()
	identity := newSignupService(t, fake, "", service.Signup{Enabled: true})
	signUp := func(from string) error {
		_, err := identity.SignInOIDC(t.Context(), "https://login.example/",
			verified("s", "a@example.com", "Ada"), from, "", "")

		return err
	}

	for range 5 {
		require.NoError(t, signUp("192.0.2.1"))
	}

	assert.ErrorIs(t, signUp("192.0.2.1"), service.ErrSignUpRateLimited)
	assert.NoError(t, signUp("192.0.2.2"), "another address has its own allowance")
}

func TestSignUpNamesTheOrganization(t *testing.T) {
	tests := []struct{ name, email, want string }{
		{"Ada Lovelace", "ada@example.com", "Ada Lovelace"},
		{"", "ada@example.com", "ada"},
		{"  ", "grace.hopper@example.com", "grace.hopper"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			fake := newFakeIdentityStore()
			identity := newSignupService(t, fake, "", service.Signup{Enabled: true, OrganizationCeiling: 10})

			_, err := identity.SignInOIDC(t.Context(), "https://login.example/",
				verified("s", tc.email, tc.name), "192.0.2.1", "", "")
			require.NoError(t, err)

			require.Len(t, fake.created, 1)
			assert.Equal(t, tc.want, fake.created[0].OrganizationName)
			assert.Equal(t, 3, fake.created[0].PerPersonCap)
			assert.Equal(t, 10, fake.created[0].OrganizationCeiling)
		})
	}
}

func TestSignUpUsesTheOrganizationNameThePersonChose(t *testing.T) {
	fake := newFakeIdentityStore()
	identity := newSignupService(t, fake, "", service.Signup{Enabled: true, OrganizationCeiling: 10})

	_, err := identity.SignInOIDC(t.Context(), "https://login.example/",
		verified("s", "ada@example.com", "Ada Lovelace"), "192.0.2.1", "", "  Analytical Engines  ")
	require.NoError(t, err)

	require.Len(t, fake.created, 1)
	assert.Equal(t, "Analytical Engines", fake.created[0].OrganizationName)
}

func TestSignUpRefusesAChosenOrganizationNameThatIsTooLong(t *testing.T) {
	fake := newFakeIdentityStore()
	identity := newSignupService(t, fake, "", service.Signup{Enabled: true, OrganizationCeiling: 10})

	_, err := identity.SignInOIDC(t.Context(), "https://login.example/",
		verified("s", "ada@example.com", "Ada"), "192.0.2.1", "", strings.Repeat("a", 201))

	assert.ErrorIs(t, err, service.ErrOrganizationName)
	assert.Empty(t, fake.created)
}
