package store_test

import (
	_ "embed"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/store"
)

// legacySchema is the schema of the release before Organizations, dumped from a
// database that release built: every row keyed by a tenant id, and the logins,
// invite links and sessions in tenant tables.
//
//go:embed testdata/release_before_organizations.sql
var legacySchema string

// legacyInstall is a database from the release before Organizations, in a
// schema of its own.
type legacyInstall struct {
	*install
	cipher *store.Cipher
}

func newLegacyInstall(t *testing.T) *legacyInstall {
	t.Helper()

	i := newInstall(t)
	admin := i.open("")
	for _, statement := range strings.Split(legacySchema, ";\n") {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		require.NoError(t, admin.Gorm().WithContext(t.Context()).Exec(statement).Error, statement)
	}
	// An earlier build of this release kept repository claims, and a database it
	// already carried over holds them.
	require.NoError(t, admin.Gorm().WithContext(t.Context()).Exec(
		`CREATE TABLE hub_repo_claims (
			name varchar(100) PRIMARY KEY, owner varchar(400) NOT NULL, created_at timestamptz NOT NULL)`).Error)
	cipher, err := store.NewCipher(testKey(90))
	require.NoError(t, err)

	return &legacyInstall{install: i, cipher: cipher}
}

func (l *legacyInstall) exec(statement string, args ...any) {
	l.t.Helper()

	db := l.open("")
	require.NoError(l.t, db.Gorm().WithContext(l.t.Context()).Exec(statement, args...).Error, statement)
}

// openWith connects the way the adapter does, with the state key, as a
// deployment that serves organization.
func (l *legacyInstall) openWith(organization string, cipher *store.Cipher) *store.Store {
	l.t.Helper()

	db, err := store.Open(l.t.Context(), store.Options{URL: l.url, Organization: organization, Cipher: cipher})
	require.NoError(l.t, err)
	l.t.Cleanup(func() { _ = db.Close() })

	return db
}

// seedTenant writes one tenant the way the old release did: a login with a hash
// and a failure count, a signed in browser, an unspent invite link, an
// environment, a join token, queued work, a sealed clone identity and
// connection, an activity row, a sealed chat turn and a claimed repository.
func (l *legacyInstall) seedTenant(tenant, hash string, failures int) {
	l.t.Helper()

	now := time.Now().UTC()
	later := now.Add(time.Hour)
	l.exec(`INSERT INTO hub_tenant_credentials VALUES (?, ?, ?, ?, ?)`, tenant, hash, failures, now, now)
	l.exec(`INSERT INTO hub_tenant_sessions (hash, tenant_id, created_at, last_seen_at, expires_at)
		VALUES (?, ?, ?, ?, ?)`, "browser-"+tenant, tenant, now, now, later)
	l.exec(`INSERT INTO hub_tenant_invites VALUES (?, ?, ?, NULL, ?)`, "link-"+tenant, tenant, later, now)
	l.exec(`INSERT INTO hub_environments (tenant_id, name, document, updated_at) VALUES (?, 'prod', '{"namespace":"pds"}', ?)`,
		tenant, now)
	l.exec(`INSERT INTO hub_tokens (tenant_id, hash, document, updated_at) VALUES (?, ?, '{}', ?)`, tenant, "token-"+tenant, now)
	l.exec(`INSERT INTO hub_work (tenant_id, environment, document, updated_at) VALUES (?, 'prod', '[]', ?)`, tenant, now)
	l.exec(`INSERT INTO hub_history (tenant_id, database_id, kind, at) VALUES (?, 'db', 'action', ?)`, tenant, now)
	l.exec(`INSERT INTO hub_imports VALUES (?, 'clusters', ?)`, tenant, now)
	l.exec(`INSERT INTO hub_repo_claims VALUES (?, ?, ?)`, tenant+"-prod", tenant+"/prod", now)

	cloneKey, err := l.cipher.Seal("hub_clone_keys\x1f"+tenant+"\x1fprod", "identity-"+tenant)
	require.NoError(l.t, err)
	l.exec(`INSERT INTO hub_clone_keys (tenant_id, environment, sealed, updated_at) VALUES (?, 'prod', ?, ?)`,
		tenant, cloneKey, now)
	connection, err := l.cipher.Seal("hub_connections\x1f"+tenant+"\x1fservicenow\x1fSN_API_KEY", "key-"+tenant)
	require.NoError(l.t, err)
	l.exec(`INSERT INTO hub_connections (tenant_id, name, field, sealed, updated_at)
		VALUES (?, 'servicenow', 'SN_API_KEY', ?, ?)`, tenant, connection, now)

	chat := "chat-" + tenant
	title, err := l.cipher.Seal("hub_chat_conversations\x1f"+tenant+"\x1f"+chat+"\x1ftitle", "title-"+tenant)
	require.NoError(l.t, err)
	l.exec(`INSERT INTO hub_chat_conversations (id, tenant_id, title, provider, model, created_at, updated_at)
		VALUES (?, ?, ?, 'openai', 'gpt', ?, ?)`, chat, tenant, title, now, now)
	content, err := l.cipher.Seal("hub_chat_messages\x1f"+tenant+"\x1f"+chat+"\x1f1\x1fuser\x1fcontent", "hello "+tenant)
	require.NoError(l.t, err)
	actions, err := l.cipher.Seal("hub_chat_messages\x1f"+tenant+"\x1f"+chat+"\x1f1\x1fuser\x1factions", "[]")
	require.NoError(l.t, err)
	l.exec(`INSERT INTO hub_chat_messages (conversation_id, tenant_id, sequence, role, content, actions, created_at)
		VALUES (?, ?, 1, 'user', ?, ?, ?)`, chat, tenant, content, actions, now)
}

func hasColumn(t *testing.T, db *store.Store, table, column string) bool {
	t.Helper()

	var n int64
	require.NoError(t, db.Gorm().WithContext(t.Context()).Raw(
		`SELECT count(*) FROM information_schema.columns
		 WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?`, table, column).Scan(&n).Error)

	return n > 0
}

func TestUpgradeTurnsATenantIntoAnOrganizationWithItsLoginAndState(t *testing.T) {
	l := newLegacyInstall(t)
	l.seedTenant("acme", "$2a$hash", 2)
	db := l.openWith("acme", l.cipher)
	ctx := t.Context()

	require.NoError(t, db.Migrate(ctx))

	var org store.Organization
	require.NoError(t, db.Gorm().First(&org).Error)
	assert.Equal(t, "acme", org.Name)
	team, err := db.Identity().ServedTeam(ctx, "acme")
	require.NoError(t, err)
	assert.Equal(t, "default", team.Name)

	var user store.User
	require.NoError(t, db.Gorm().First(&user).Error)
	assert.Equal(t, "password", user.Issuer)
	assert.Equal(t, "acme", user.Subject)
	assert.Equal(t, "$2a$hash", user.PasswordHash, "the old login keeps working")
	assert.Equal(t, 2, user.Failures)
	placement, err := db.Identity().Placement(ctx, "acme")
	require.NoError(t, err)
	assert.Equal(t, store.RoleAdmin, placement.Role)
	assert.Equal(t, team.ID, placement.TeamID)

	var session store.Session
	require.NoError(t, db.Gorm().Where("hash = ?", "browser-acme").Take(&session).Error)
	assert.Equal(t, user.ID, session.UserID, "the browser is still signed in")
	assert.Equal(t, org.ID, session.OrganizationID)
	username, err := db.Identity().Invite(ctx, "link-acme", time.Now())
	require.NoError(t, err)
	assert.Equal(t, "acme", username, "an unspent link still names the login it sets a password for")

	state := db.Hub(team.ID, l.cipher)
	environments, err := state.Environments(ctx)
	require.NoError(t, err)
	assert.JSONEq(t, `{"namespace":"pds","repo":"prod"}`, environments["prod"],
		"the repository the environment already uses is written onto its record")
	tokens, err := state.Tokens(ctx)
	require.NoError(t, err)
	assert.Contains(t, tokens, "token-acme")
	work, err := state.Work(ctx)
	require.NoError(t, err)
	assert.Contains(t, work, "prod")

	cloneKeys, err := state.CloneKeys(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"prod": "identity-acme"}, cloneKeys, "sealed values open against the Team")
	connection, err := state.Connection(ctx, "servicenow")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"SN_API_KEY": "key-acme"}, connection, "and against the Organization")
	conversation, err := state.ChatConversation(ctx, "chat-acme")
	require.NoError(t, err)
	assert.Equal(t, "title-acme", conversation.Title)
	messages, err := state.ChatMessages(ctx, "chat-acme")
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, "hello acme", messages[0].Content)

	won, fresh, err := db.ClaimRepo(ctx, "acme-prod", fmt.Sprintf("%d/prod", team.ID))
	require.NoError(t, err)
	assert.True(t, won, "the claim on a repository follows its owner to the Team")
	assert.False(t, fresh)
}

func TestUpgradeDropsTheTenantColumnsAndTables(t *testing.T) {
	l := newLegacyInstall(t)
	l.seedTenant("acme", "$2a$hash", 0)
	db := l.openWith("acme", l.cipher)

	require.NoError(t, db.Migrate(t.Context()))

	for _, table := range []string{
		"hub_environments", "hub_tokens", "hub_work", "hub_clone_keys", "hub_connections",
		"hub_history", "hub_chat_conversations", "hub_chat_messages", "hub_sessions",
	} {
		assert.False(t, hasColumn(t, db, table, "tenant_id"), table)
	}
	for _, table := range []string{"hub_tenant_credentials", "hub_tenant_invites", "hub_imports", "hub_tenant_sessions"} {
		assert.False(t, db.Gorm().Migrator().HasTable(table), table)
	}
	assert.False(t, hasColumn(t, db, "hub_teams", "key"))
}

func TestUpgradeTwiceChangesNothing(t *testing.T) {
	l := newLegacyInstall(t)
	l.seedTenant("acme", "$2a$hash", 0)
	db := l.openWith("acme", l.cipher)

	require.NoError(t, db.Migrate(t.Context()))
	require.NoError(t, db.Migrate(t.Context()))

	for name, model := range map[string]any{
		"organizations": &store.Organization{}, "teams": &store.Team{}, "users": &store.User{},
		"memberships": &store.Membership{}, "invites": &store.PasswordInvite{},
	} {
		assert.Equal(t, int64(1), count(t, db, model, "1 = 1"), name)
	}
}

func TestUpgradeServingEveryOrganizationKeepsEachTenantApart(t *testing.T) {
	l := newLegacyInstall(t)
	l.seedTenant("acme", "$2a$hash", 0)
	l.seedTenant("globex", "$2a$other", 0)
	db := l.openWith("*", l.cipher)
	ctx := t.Context()

	require.NoError(t, db.Migrate(ctx))

	assert.Equal(t, int64(2), count(t, db, &store.Organization{}, "1 = 1"))
	assert.Equal(t, int64(2), count(t, db, &store.User{}, "issuer = ?", "password"))
	placement, err := db.Identity().Placement(ctx, "globex")
	require.NoError(t, err)
	environments, err := db.Hub(placement.TeamID, l.cipher).Environments(ctx)
	require.NoError(t, err)
	assert.JSONEq(t, `{"namespace":"pds","repo":"globex-prod"}`, environments["prod"],
		"with many Organizations a repository is named after the tenant that owns it")
	connection, err := db.Hub(placement.TeamID, l.cipher).Connection(ctx, "servicenow")
	require.NoError(t, err)
	assert.Equal(t, "key-globex", connection["SN_API_KEY"], "each Organization keeps its own credential")
}

func TestUpgradeServingOneOrganizationRefusesAnotherTenantsRows(t *testing.T) {
	l := newLegacyInstall(t)
	l.seedTenant("acme", "$2a$hash", 0)
	l.seedTenant("globex", "$2a$other", 0)
	db := l.openWith("acme", l.cipher)

	err := db.Migrate(t.Context())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "globex")
	assert.True(t, hasColumn(t, db, "hub_environments", "tenant_id"), "a refused start leaves the database as it was")
	assert.False(t, db.Gorm().Migrator().HasTable("hub_organizations"), "and nothing was made")
}

func TestUpgradeWithoutTheStateKeyRefusesSealedValues(t *testing.T) {
	l := newLegacyInstall(t)
	l.seedTenant("acme", "$2a$hash", 0)
	db := l.openWith("acme", nil)

	err := db.Migrate(t.Context())

	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "state encryption key"))
	assert.True(t, hasColumn(t, db, "hub_clone_keys", "tenant_id"), "nothing was changed")
}

func TestUpgradeWithoutAnOrganizationRefusesRows(t *testing.T) {
	l := newLegacyInstall(t)
	l.seedTenant("acme", "$2a$hash", 0)
	db := l.openWith("", l.cipher)

	err := db.Migrate(t.Context())

	require.Error(t, err)
}

// TestUpgradeFindsTheTeamAnEarlierBuildMade covers a database an earlier build
// of this release already carried over: its Teams kept the tenant id as a key,
// and the password changes made since went to the old login row.
func TestUpgradeFindsTheTeamAnEarlierBuildMade(t *testing.T) {
	l := newLegacyInstall(t)
	l.seedTenant("acme", "$2a$new", 3)
	db := l.openWith("acme", l.cipher)
	ctx := t.Context()
	require.NoError(t, db.Gorm().WithContext(ctx).AutoMigrate(
		&store.Organization{}, &store.Team{}, &store.User{}, &store.Membership{}))
	require.NoError(t, db.Gorm().Exec(`ALTER TABLE hub_teams ADD COLUMN key varchar(128) NOT NULL DEFAULT ''`).Error)
	earlier := time.Now().UTC().Add(-time.Hour)
	org := store.Organization{Name: "acme", CreatedAt: earlier}
	require.NoError(t, db.Gorm().Create(&org).Error)
	team := store.Team{OrganizationID: org.ID, Name: "default", CreatedAt: earlier}
	require.NoError(t, db.Gorm().Create(&team).Error)
	require.NoError(t, db.Gorm().Exec(`UPDATE hub_teams SET key = 'acme' WHERE id = ?`, team.ID).Error)
	user := store.User{
		Issuer: "password", Subject: "acme", Username: "acme", PasswordHash: "$2a$old",
		CreatedAt: earlier, UpdatedAt: earlier,
	}
	require.NoError(t, db.Gorm().Create(&user).Error)
	require.NoError(t, db.Gorm().Create(&store.Membership{
		UserID: user.ID, OrganizationID: org.ID, TeamID: team.ID, Role: store.RoleAdmin, CreatedAt: earlier,
	}).Error)

	// A Team that signed up under the earlier build, with rows of its own and a
	// connection named like its sibling's.
	payments := store.Team{OrganizationID: org.ID, Name: "payments", CreatedAt: earlier}
	require.NoError(t, db.Gorm().Create(&payments).Error)
	require.NoError(t, db.Gorm().Exec(`UPDATE hub_teams SET key = 'team-9' WHERE id = ?`, payments.ID).Error)
	l.exec(`INSERT INTO hub_environments (tenant_id, name, document, updated_at) VALUES ('team-9', 'pay', '{}', now())`)
	l.exec(`INSERT INTO hub_connections (tenant_id, name, field, sealed, updated_at)
		VALUES ('team-9', 'servicenow', 'SN_API_KEY', 'older', now() - interval '1 day')`)

	require.NoError(t, db.Migrate(ctx), "another Team's rows are not another deployment's")

	assert.Equal(t, int64(1), count(t, db, &store.Organization{}, "1 = 1"), "no second Organization was made")
	paid, err := db.Hub(payments.ID, l.cipher).Environments(ctx)
	require.NoError(t, err)
	assert.Contains(t, paid, "pay", "the Team's rows follow it")
	assert.Equal(t, int64(1), count(t, db, &store.HubConnection{}, "organization_id = ? AND name = ?", org.ID, "servicenow"),
		"the Organization holds one copy of a connection its Teams each held")
	var got store.User
	require.NoError(t, db.Gorm().Take(&got, user.ID).Error)
	assert.Equal(t, "$2a$new", got.PasswordHash, "the newer password wins")
	assert.Equal(t, 3, got.Failures)
	environments, err := db.Hub(team.ID, l.cipher).Environments(ctx)
	require.NoError(t, err)
	assert.Contains(t, environments, "prod", "the rows moved to the Team the earlier build made")
	assert.False(t, hasColumn(t, db, "hub_teams", "key"))
}
