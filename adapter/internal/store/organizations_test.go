package store_test

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/store"
)

// install is an empty PostgreSQL schema of its own, so a test can build a
// deployment without the shared test database's other rows.
type install struct {
	t   *testing.T
	url string
}

func newInstall(t *testing.T) *install {
	t.Helper()

	base := liveURL(t)
	name := fmt.Sprintf("install_%d", time.Now().UnixNano())

	admin, err := store.Open(t.Context(), store.Options{URL: base})
	require.NoError(t, err)
	require.NoError(t, admin.Gorm().WithContext(t.Context()).Exec("CREATE SCHEMA "+name).Error)
	t.Cleanup(func() {
		_ = admin.Gorm().Exec("DROP SCHEMA " + name + " CASCADE").Error
		_ = admin.Close()
	})

	parsed, err := url.Parse(base)
	require.NoError(t, err)
	query := parsed.Query()
	query.Set("search_path", name)
	parsed.RawQuery = query.Encode()

	return &install{t: t, url: parsed.String()}
}

// open connects as a deployment that serves the named Organization, or every
// one for a star, or none for an empty name.
func (i *install) open(organization string) *store.Store {
	i.t.Helper()

	db, err := store.Open(i.t.Context(), store.Options{URL: i.url, Organization: organization})
	require.NoError(i.t, err)
	i.t.Cleanup(func() { _ = db.Close() })

	return db
}

// seed makes a customer: a login with its own Organization, and an environment,
// a join token, a connection and an activity row under its Team.
func (i *install) seed(login, hash string, failures int) {
	i.t.Helper()

	db := i.open("*")
	ctx := i.t.Context()
	require.NoError(i.t, db.Migrate(ctx))
	_, err := db.Identity().EnsureLogin(ctx, login, time.Now())
	require.NoError(i.t, err)
	require.NoError(i.t, db.Identity().SetPassword(ctx, login, hash, time.Now()))
	require.NoError(i.t, db.Gorm().WithContext(ctx).Model(&store.User{}).
		Where("issuer = ? AND subject = ?", "password", login).Update("failures", failures).Error)

	placement, err := db.Identity().Placement(ctx, login)
	require.NoError(i.t, err)
	now := time.Now().UTC()
	for _, row := range []any{
		&store.HubEnvironment{TeamID: placement.TeamID, Name: "eks-prod", Document: `{}`, UpdatedAt: now},
		&store.HubToken{TeamID: placement.TeamID, Hash: "hash-" + login, Document: `{}`, UpdatedAt: now},
		&store.HubConnection{
			OrganizationID: placement.OrganizationID, Name: "servicenow", Field: "key", Sealed: "sealed", UpdatedAt: now,
		},
		&store.HistoryRow{TeamID: placement.TeamID, DatabaseID: "db", Kind: "action", At: now},
	} {
		require.NoError(i.t, db.Gorm().WithContext(ctx).Create(row).Error)
	}
}

func count(t *testing.T, db *store.Store, model any, where string, args ...any) int64 {
	t.Helper()

	var n int64
	require.NoError(t, db.Gorm().WithContext(context.Background()).Model(model).Where(where, args...).Count(&n).Error)

	return n
}

func TestMigrateMakesTheServedOrganizationWhenNoneExists(t *testing.T) {
	db := newInstall(t).open("acme")
	require.NoError(t, db.Migrate(t.Context()))

	var org store.Organization
	require.NoError(t, db.Gorm().First(&org).Error)
	assert.Equal(t, "acme", org.Name)
	assert.Equal(t, int64(1), count(t, db, &store.Team{}, "organization_id = ? AND name = ?", org.ID, "default"))
	assert.Equal(t, int64(0), count(t, db, &store.User{}, "1 = 1"), "nobody has a login yet")
}

func TestMigrateTwiceChangesNothing(t *testing.T) {
	db := newInstall(t).open("acme")
	require.NoError(t, db.Migrate(t.Context()))
	require.NoError(t, db.Migrate(t.Context()))

	assert.Equal(t, int64(1), count(t, db, &store.Organization{}, "1 = 1"))
	assert.Equal(t, int64(1), count(t, db, &store.Team{}, "1 = 1"))
}

func TestMigrateServingEveryOrganizationMakesNone(t *testing.T) {
	for _, served := range []string{"", "*"} {
		db := newInstall(t).open(served)
		require.NoError(t, db.Migrate(t.Context()))

		assert.Equal(t, int64(0), count(t, db, &store.Organization{}, "1 = 1"), "served %q", served)
	}
}

func TestALoginJoinsTheServedOrganization(t *testing.T) {
	db := newInstall(t).open("acme")
	ctx := t.Context()
	require.NoError(t, db.Migrate(ctx))

	created, err := db.Identity().EnsureLogin(ctx, "ada", time.Now())
	require.NoError(t, err)
	require.True(t, created)

	placement, err := db.Identity().Placement(ctx, "ada")
	require.NoError(t, err)
	assert.Equal(t, "acme", placement.OrganizationName, "the login joins the Organization this deployment serves")
	assert.Equal(t, store.RoleAdmin, placement.Role)
	assert.Equal(t, int64(1), count(t, db, &store.Organization{}, "1 = 1"), "no second Organization was made")
}

func TestMigrateStartsEveryOrganizationAtReadWithPasswordsAndSQLWritesOff(t *testing.T) {
	db := newInstall(t).open("*")
	ctx := t.Context()
	require.NoError(t, db.Migrate(ctx))
	for _, login := range []string{"ada", "bob"} {
		_, err := db.Identity().EnsureLogin(ctx, login, time.Now())
		require.NoError(t, err)
	}
	// An Organization made by the release before the AI settings has none of
	// the columns, so take them away and migrate again.
	require.NoError(t, db.Gorm().WithContext(ctx).Exec(
		"ALTER TABLE hub_organizations DROP COLUMN ai_access, DROP COLUMN ai_passwords, DROP COLUMN ai_sql_writes").Error)

	require.NoError(t, db.Migrate(ctx))

	var orgs []store.Organization
	require.NoError(t, db.Gorm().WithContext(ctx).Find(&orgs).Error)
	require.Len(t, orgs, 2)
	for _, org := range orgs {
		got, err := db.Identity().OrganizationAI(ctx, org.ID)
		require.NoError(t, err)
		assert.Equal(t, store.AISettings{Access: "read"}, got, "organization %d", org.ID)
	}
}

func TestOrganizationAIIsSetPerOrganization(t *testing.T) {
	db := newInstall(t).open("*")
	ctx := t.Context()
	require.NoError(t, db.Migrate(ctx))
	for _, login := range []string{"ada", "bob"} {
		_, err := db.Identity().EnsureLogin(ctx, login, time.Now())
		require.NoError(t, err)
	}
	ada, err := db.Identity().Placement(ctx, "ada")
	require.NoError(t, err)
	bob, err := db.Identity().Placement(ctx, "bob")
	require.NoError(t, err)

	require.NoError(t, db.Identity().SetOrganizationAI(ctx, ada.OrganizationID,
		store.AISettings{Access: "change_delete", Passwords: true, SQLWrites: true}))

	got, err := db.Identity().OrganizationAI(ctx, ada.OrganizationID)
	require.NoError(t, err)
	assert.Equal(t, store.AISettings{Access: "change_delete", Passwords: true, SQLWrites: true}, got)

	require.NoError(t, db.Identity().SetOrganizationAI(ctx, ada.OrganizationID, store.AISettings{Access: "read"}))
	got, err = db.Identity().OrganizationAI(ctx, ada.OrganizationID)
	require.NoError(t, err)
	assert.Equal(t, store.AISettings{Access: "read"}, got, "switches turn off again")

	other, err := db.Identity().OrganizationAI(ctx, bob.OrganizationID)
	require.NoError(t, err)
	assert.Equal(t, store.AISettings{Access: "read"}, other)

	_, err = db.Identity().OrganizationAI(ctx, 9999)
	require.ErrorIs(t, err, store.ErrOrganizationUnknown)
	require.ErrorIs(t, db.Identity().SetOrganizationAI(ctx, 9999, store.AISettings{Access: "read"}), store.ErrOrganizationUnknown)
}
