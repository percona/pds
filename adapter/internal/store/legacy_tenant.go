package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// This file is the whole of the upgrade from the release before Organizations,
// whose rows were keyed by a tenant id and whose logins, invite links and
// sessions lived in tenant tables. It names those old shapes and nothing else
// does. Deleting the file, and the call to contractLegacySchema, ends the
// window for installs that have not reached this revision yet.
//
// The upgrade is expand and contract in one step: each tenant becomes an
// Organization with a default Team and an admin User, every row is pointed at
// that Team, sealed values are re-sealed against it, and the tenant columns and
// tables are dropped. After it the release before Organizations cannot run
// against the database again.

// Every table and column name that reaches a statement below comes from a
// constant in this file, never from input. A tenant id only ever travels as a
// bound parameter.

// legacyTeamTables are the tables whose rows a tenant owned and a Team owns now,
// with the columns that were their primary key besides the tenant id. A table
// with no listed key has a numeric id instead.
var legacyTeamTables = []struct {
	table string
	key   []string
}{
	{"hub_environments", []string{"name"}},
	{"hub_tokens", []string{"hash"}},
	{"hub_work", []string{"environment"}},
	{"hub_clone_keys", []string{"environment"}},
	{"hub_history", nil},
	{"hub_usage_samples", nil},
	{"hub_join_phases", nil},
	{"hub_chat_conversations", nil},
	{"hub_chat_messages", nil},
}

// legacyOwner is one tenant and the Team and Organization it became.
type legacyOwner struct {
	tenant       string
	teamID       uint
	organization uint
}

// contractLegacySchema upgrades a database that still carries tenant columns,
// in one transaction under the schema lock, and does nothing to one that does
// not. It runs before the models are reconciled, because that step adds
// columns and never changes a primary key.
func (s *Store) contractLegacySchema(ctx context.Context) error {
	legacy, err := s.hasLegacyTenants(ctx)
	if err != nil || !legacy {
		return err
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.AutoMigrate(&Organization{}, &Team{}, &User{}, &Membership{}, &PasswordInvite{}); err != nil {
			return fmt.Errorf("make the organization tables: %w", err)
		}
		if err := renameLegacySessions(tx); err != nil {
			return err
		}
		// The release before Organizations kept no user on a session. The
		// columns are added before the sessions are carried over.
		if err := tx.AutoMigrate(&Session{}); err != nil {
			return fmt.Errorf("make the session columns: %w", err)
		}

		owners, err := s.legacyOwners(tx)
		if err != nil {
			return err
		}
		if err := carryLogins(tx, owners); err != nil {
			return err
		}
		if err := s.reseal(tx, owners); err != nil {
			return err
		}
		if err := recordRepositories(tx, owners, s.organization == everyOrganization); err != nil {
			return err
		}
		if err := repointRows(tx, owners); err != nil {
			return err
		}

		return dropLegacyTables(tx)
	})
}

// hasLegacyTenants reports whether any table still has its tenant column.
func (s *Store) hasLegacyTenants(ctx context.Context) (bool, error) {
	var n int64
	err := s.db.WithContext(ctx).Raw(`SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND column_name = 'tenant_id'`).Scan(&n).Error
	if err != nil {
		return false, fmt.Errorf("look for tenant columns: %w", err)
	}

	return n > 0, nil
}

func renameLegacySessions(tx *gorm.DB) error {
	migrator := tx.Migrator()
	if migrator.HasTable("hub_tenant_sessions") && !migrator.HasTable("hub_sessions") {
		if err := migrator.RenameTable("hub_tenant_sessions", "hub_sessions"); err != nil {
			return fmt.Errorf("rename the session table: %w", err)
		}
	}

	return nil
}

// legacyOwners finds the Team each tenant's rows move to. A named deployment
// serves one tenant, which becomes its Organization even when it holds no row,
// and refuses a database that holds another's. A star gives each tenant an
// Organization of its own. A Team an earlier build of this release already made
// is found again by the key it kept, in either mode, so a second run changes
// nothing and the Teams of an Organization that signed up are not mistaken for
// another deployment's rows.
func (s *Store) legacyOwners(tx *gorm.DB) ([]legacyOwner, error) {
	tenants, err := legacyTenantIDs(tx)
	if err != nil {
		return nil, err
	}

	known, err := keyedTeams(tx)
	if err != nil {
		return nil, err
	}

	switch {
	case s.organization == "":
		if len(tenants) > 0 {
			return nil, errors.New("the database holds rows from the release before organizations, " +
				"but no organization was configured to take them")
		}

		return nil, nil
	case s.organization != everyOrganization:
		var others []string
		for _, id := range tenants {
			if _, made := known[id]; id != s.organization && !made {
				others = append(others, id)
			}
		}
		if len(others) > 0 {
			return nil, fmt.Errorf("legacy rows exist for tenants %s, but this deployment serves only %q",
				strings.Join(others, ", "), s.organization)
		}
		kept := []string{s.organization}
		for _, id := range tenants {
			if _, made := known[id]; made && id != s.organization {
				kept = append(kept, id)
			}
		}
		tenants = kept
	}

	now := time.Now().UTC()
	owners := make([]legacyOwner, 0, len(tenants))
	for _, tenant := range tenants {
		if owner, ok := known[tenant]; ok {
			owners = append(owners, owner)

			continue
		}
		org, team, err := s.legacyOrganization(tx, tenant, now)
		if err != nil {
			return nil, err
		}
		owners = append(owners, legacyOwner{tenant: tenant, teamID: team.ID, organization: org.ID})
	}

	return owners, nil
}

// legacyOrganization makes the Organization a tenant becomes. The served
// Organization of a named deployment is the first one made, so a login created
// before it joins that one rather than a duplicate.
func (s *Store) legacyOrganization(tx *gorm.DB, tenant string, now time.Time) (Organization, Team, error) {
	var org Organization
	err := tx.Where("name = ?", tenant).Order("id").Take(&org).Error
	if err == nil {
		var team Team
		if err := tx.Where("organization_id = ?", org.ID).Order("id").Take(&team).Error; err != nil {
			return Organization{}, Team{}, fmt.Errorf("read the team of organization %q: %w", tenant, err)
		}

		return org, team, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return Organization{}, Team{}, fmt.Errorf("read organization %q: %w", tenant, err)
	}

	return newOrganization(tx, tenant, now)
}

// keyedTeams maps a tenant to the Team an earlier build of this release gave
// it, when that build left its key on the Team.
func keyedTeams(tx *gorm.DB) (map[string]legacyOwner, error) {
	out := map[string]legacyOwner{}
	if !tx.Migrator().HasColumn("hub_teams", "key") {
		return out, nil
	}

	var rows []struct {
		ID             uint
		OrganizationID uint
		Key            string
	}
	if err := tx.Raw(`SELECT id, organization_id, key FROM hub_teams WHERE key <> '' ORDER BY id`).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("read team keys: %w", err)
	}
	for _, row := range rows {
		if _, seen := out[row.Key]; !seen {
			out[row.Key] = legacyOwner{tenant: row.Key, teamID: row.ID, organization: row.OrganizationID}
		}
	}

	return out, nil
}

// legacyTenantIDs lists every tenant id that owns a row, in a stable order.
func legacyTenantIDs(tx *gorm.DB) ([]string, error) {
	seen := map[string]bool{}
	tables := []string{"hub_connections", "hub_tenant_credentials"}
	for _, t := range legacyTeamTables {
		tables = append(tables, t.table)
	}

	for _, table := range tables {
		if !tx.Migrator().HasTable(table) || !tx.Migrator().HasColumn(table, "tenant_id") {
			continue
		}
		var ids []string
		if err := tx.Raw("SELECT DISTINCT tenant_id FROM " + table).Scan(&ids).Error; err != nil {
			return nil, fmt.Errorf("list tenants of %s: %w", table, err)
		}
		for _, id := range ids {
			seen[id] = true
		}
	}

	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)

	return out, nil
}

// carryLogins turns each tenant's login into the admin User of its default
// Team, and keeps what that login held working: its password and failure count,
// its signed in browsers and its invite links. It can run again over a database
// an earlier build already carried: a User is only made when it is missing, and
// the hash and failure count of the login are copied only when it changed after
// the User did.
func carryLogins(tx *gorm.DB, owners []legacyOwner) error {
	if !tx.Migrator().HasTable("hub_tenant_credentials") {
		return nil
	}

	now := time.Now().UTC()
	for _, owner := range owners {
		var credential struct {
			PasswordHash string
			Failures     int
			CreatedAt    time.Time
			UpdatedAt    time.Time
		}
		err := tx.Raw(`SELECT password_hash, failures, created_at, updated_at
			FROM hub_tenant_credentials WHERE tenant_id = ?`, owner.tenant).Scan(&credential).Error
		if err != nil {
			return fmt.Errorf("read the login of tenant %q: %w", owner.tenant, err)
		}
		if credential.CreatedAt.IsZero() {
			continue
		}

		var user User
		err = tx.Where("issuer = ? AND subject = ?", passwordIssuer, owner.tenant).Take(&user).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			var org Organization
			if err := tx.Take(&org, owner.organization).Error; err != nil {
				return fmt.Errorf("read organization of tenant %q: %w", owner.tenant, err)
			}
			team := Team{ID: owner.teamID, OrganizationID: owner.organization}
			user, err = addLogin(tx, org, team, owner.tenant, loginSeed{
				hash: credential.PasswordHash, failures: credential.Failures, createdAt: credential.CreatedAt,
			}, now)
			if err != nil {
				return err
			}
		case err != nil:
			return fmt.Errorf("read the user of tenant %q: %w", owner.tenant, err)
		case credential.UpdatedAt.After(user.UpdatedAt):
			if err := tx.Model(&User{}).Where("id = ?", user.ID).Updates(map[string]any{
				"password_hash": credential.PasswordHash, "failures": credential.Failures, "updated_at": credential.UpdatedAt,
			}).Error; err != nil {
				return fmt.Errorf("carry the password of tenant %q: %w", owner.tenant, err)
			}
		}

		if err := carryLoginState(tx, owner, user); err != nil {
			return err
		}
	}

	return nil
}

// carryLoginState points a tenant's signed in browsers at its User and copies
// its invite links, so an upgrade neither signs anybody out nor spends a link.
func carryLoginState(tx *gorm.DB, owner legacyOwner, user User) error {
	if tx.Migrator().HasTable("hub_sessions") {
		if err := tx.Exec(`UPDATE hub_sessions SET user_id = ?, organization_id = ?
			WHERE tenant_id = ? AND user_id = 0`, user.ID, owner.organization, owner.tenant).Error; err != nil {
			return fmt.Errorf("carry the sessions of tenant %q: %w", owner.tenant, err)
		}
	}

	if !tx.Migrator().HasTable("hub_tenant_invites") {
		return nil
	}
	var invites []struct {
		Hash      string
		ExpiresAt time.Time
		ClaimedAt *time.Time
		CreatedAt time.Time
	}
	if err := tx.Raw(`SELECT hash, expires_at, claimed_at, created_at
		FROM hub_tenant_invites WHERE tenant_id = ?`, owner.tenant).Scan(&invites).Error; err != nil {
		return fmt.Errorf("read the invites of tenant %q: %w", owner.tenant, err)
	}
	for _, invite := range invites {
		row := PasswordInvite{
			Hash: invite.Hash, UserID: user.ID, ExpiresAt: invite.ExpiresAt,
			ClaimedAt: invite.ClaimedAt, CreatedAt: invite.CreatedAt,
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return fmt.Errorf("carry an invite of tenant %q: %w", owner.tenant, err)
		}
	}

	return nil
}

// reseal opens every value sealed against a tenant and seals it against the
// Team or Organization that replaces it, because the binding is part of what a
// sealed value authenticates. It needs the key the values were sealed with and
// refuses to go on without it rather than leave them unreadable.
func (s *Store) reseal(tx *gorm.DB, owners []legacyOwner) error {
	for _, owner := range owners {
		team, organization := strconv.FormatUint(uint64(owner.teamID), 10), strconv.FormatUint(uint64(owner.organization), 10)
		sources := []resealTable{
			{"hub_clone_keys", []string{"environment"}, "sealed", func(c []string) (string, string) {
				return "hub_clone_keys\x1f" + owner.tenant + "\x1f" + c[0], "hub_clone_keys\x1f" + team + "\x1f" + c[0]
			}},
			{"hub_connections", []string{"name", "field"}, "sealed", func(c []string) (string, string) {
				return "hub_connections\x1f" + owner.tenant + "\x1f" + c[0] + "\x1f" + c[1],
					"hub_connections\x1f" + organization + "\x1f" + c[0] + "\x1f" + c[1]
			}},
			{"hub_chat_conversations", []string{"id"}, "title", func(c []string) (string, string) {
				return "hub_chat_conversations\x1f" + owner.tenant + "\x1f" + c[0] + "\x1ftitle",
					"hub_chat_conversations\x1f" + team + "\x1f" + c[0] + "\x1ftitle"
			}},
		}
		for _, source := range sources {
			if err := s.resealTable(tx, owner.tenant, source); err != nil {
				return err
			}
		}
		if err := s.resealMessages(tx, owner.tenant, team); err != nil {
			return err
		}
	}

	return nil
}

// resealTable is one sealed column and how its binding changes.
type resealTable struct {
	table   string
	keys    []string
	column  string
	binding func(keys []string) (old, current string)
}

func (s *Store) resealTable(tx *gorm.DB, tenant string, t resealTable) error {
	if !tx.Migrator().HasTable(t.table) {
		return nil
	}

	rows, err := tx.Raw("SELECT "+strings.Join(t.keys, ", ")+", "+t.column+" FROM "+t.table+
		" WHERE tenant_id = ?", tenant).Rows()
	if err != nil {
		return fmt.Errorf("read %s of tenant %q: %w", t.table, tenant, err)
	}
	type pending struct {
		keys   []string
		sealed string
	}
	var batch []pending
	for rows.Next() {
		keys := make([]string, len(t.keys))
		dest := make([]any, 0, len(keys)+1)
		for i := range keys {
			dest = append(dest, &keys[i])
		}
		var sealed string
		dest = append(dest, &sealed)
		if err := rows.Scan(dest...); err != nil {
			_ = rows.Close()

			return fmt.Errorf("read %s of tenant %q: %w", t.table, tenant, err)
		}
		batch = append(batch, pending{keys: keys, sealed: sealed})
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("read %s of tenant %q: %w", t.table, tenant, err)
	}

	for _, row := range batch {
		old, current := t.binding(row.keys)
		moved, err := s.moveSeal(old, current, row.sealed)
		if err != nil {
			return fmt.Errorf("re-seal a row of %s for tenant %q: %w", t.table, tenant, err)
		}
		if moved == row.sealed {
			continue
		}

		where := make([]string, 0, len(t.keys)+1)
		args := []any{moved}
		for i, key := range t.keys {
			where = append(where, key+" = ?")
			args = append(args, row.keys[i])
		}
		args = append(args, tenant)
		if err := tx.Exec("UPDATE "+t.table+" SET "+t.column+" = ? WHERE "+strings.Join(where, " AND ")+
			" AND tenant_id = ?", args...).Error; err != nil {
			return fmt.Errorf("write a row of %s for tenant %q: %w", t.table, tenant, err)
		}
	}

	return nil
}

// resealMessages re-seals the two columns of every chat turn, which are bound to
// the conversation, the turn's place in it and its role.
func (s *Store) resealMessages(tx *gorm.DB, tenant, team string) error {
	if !tx.Migrator().HasTable("hub_chat_messages") {
		return nil
	}

	var rows []struct {
		ID             uint64
		ConversationID string
		Sequence       int
		Role           string
		Content        string
		Actions        string
	}
	if err := tx.Raw(`SELECT id, conversation_id, sequence, role, content, actions
		FROM hub_chat_messages WHERE tenant_id = ?`, tenant).Scan(&rows).Error; err != nil {
		return fmt.Errorf("read chat turns of tenant %q: %w", tenant, err)
	}

	for _, row := range rows {
		suffix := "\x1f" + row.ConversationID + "\x1f" + strconv.Itoa(row.Sequence) + "\x1f" + row.Role + "\x1f"
		content, err := s.moveSeal("hub_chat_messages\x1f"+tenant+suffix+"content",
			"hub_chat_messages\x1f"+team+suffix+"content", row.Content)
		if err != nil {
			return fmt.Errorf("re-seal a chat turn for tenant %q: %w", tenant, err)
		}
		actions, err := s.moveSeal("hub_chat_messages\x1f"+tenant+suffix+"actions",
			"hub_chat_messages\x1f"+team+suffix+"actions", row.Actions)
		if err != nil {
			return fmt.Errorf("re-seal a chat turn for tenant %q: %w", tenant, err)
		}
		if err := tx.Exec(`UPDATE hub_chat_messages SET content = ?, actions = ? WHERE id = ?`,
			content, actions, row.ID).Error; err != nil {
			return fmt.Errorf("write a chat turn for tenant %q: %w", tenant, err)
		}
	}

	return nil
}

// moveSeal re-seals one value from its old binding to its new one. A value that
// already opens against the new binding was moved by an earlier run and is left
// as it is, and one that is not sealed at all is left for whatever reads it.
func (s *Store) moveSeal(oldBinding, newBinding, sealed string) (string, error) {
	if !Sealed(sealed) {
		return sealed, nil
	}
	if s.cipher == nil {
		return "", errors.New("the database holds sealed values from the release before organizations, " +
			"and no state encryption key was given to re-seal them")
	}
	if _, err := s.cipher.Open(newBinding, sealed); err == nil {
		return sealed, nil
	}

	plain, err := s.cipher.Open(oldBinding, sealed)
	if err != nil {
		return "", err
	}

	return s.cipher.Seal(newBinding, plain)
}

// recordRepositories writes the repository each environment record keeps, for a
// record from before records named one. A record is never renamed after this
// point, so the name has to be the one the environment already uses: the
// environment's own name for a deployment that serves one Organization, and the
// tenant and the environment otherwise. The repositories an environment is
// claimed under follow their owner to the Team.
func recordRepositories(tx *gorm.DB, owners []legacyOwner, everyone bool) error {
	for _, owner := range owners {
		name := "name"
		if everyone {
			name = "? || '-' || name"
		}
		args := []any{}
		if everyone {
			args = append(args, owner.tenant)
		}
		args = append(args, owner.tenant)
		if err := tx.Exec(`UPDATE hub_environments
			SET document = jsonb_set(document, '{repo}', to_jsonb(`+name+`))
			WHERE tenant_id = ? AND COALESCE(document->>'repo', '') = ''`, args...).Error; err != nil {
			return fmt.Errorf("record the repositories of tenant %q: %w", owner.tenant, err)
		}

		if tx.Migrator().HasTable("hub_repo_claims") {
			if err := tx.Exec(`UPDATE hub_repo_claims SET owner = ? || substr(owner, ?)
				WHERE owner LIKE ?`, strconv.FormatUint(uint64(owner.teamID), 10), len(owner.tenant)+1,
				escapeLike(owner.tenant)+"/%").Error; err != nil {
				return fmt.Errorf("move the repository claims of tenant %q: %w", owner.tenant, err)
			}
		}
	}

	return nil
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// repointRows gives every row its Team, then drops the tenant columns. A
// primary key that held the tenant id is rebuilt on the Team, and the indexes
// that held it go with the column and are made again by the schema
// reconciliation that follows.
func repointRows(tx *gorm.DB, owners []legacyOwner) error {
	for _, t := range legacyTeamTables {
		if !tx.Migrator().HasTable(t.table) || !tx.Migrator().HasColumn(t.table, "tenant_id") {
			continue
		}
		if err := tx.Exec("ALTER TABLE " + t.table + " ADD COLUMN IF NOT EXISTS team_id bigint").Error; err != nil {
			return fmt.Errorf("add team_id to %s: %w", t.table, err)
		}
		for _, owner := range owners {
			if err := tx.Exec("UPDATE "+t.table+" SET team_id = ? WHERE tenant_id = ?", owner.teamID, owner.tenant).Error; err != nil {
				return fmt.Errorf("point %s at its team: %w", t.table, err)
			}
		}
		var orphans int64
		if err := tx.Raw("SELECT count(*) FROM " + t.table + " WHERE team_id IS NULL").Scan(&orphans).Error; err != nil {
			return fmt.Errorf("count rows of %s that belong to no team: %w", t.table, err)
		}
		if orphans > 0 {
			return fmt.Errorf("%d rows of %s belong to no team", orphans, t.table)
		}
		if err := tx.Exec("ALTER TABLE " + t.table + " ALTER COLUMN team_id SET NOT NULL").Error; err != nil {
			return fmt.Errorf("require a team in %s: %w", t.table, err)
		}
		if err := tx.Exec("ALTER TABLE " + t.table + " DROP COLUMN tenant_id").Error; err != nil {
			return fmt.Errorf("drop tenant_id from %s: %w", t.table, err)
		}
		if len(t.key) > 0 {
			if err := tx.Exec("ALTER TABLE " + t.table + " ADD PRIMARY KEY (team_id, " + strings.Join(t.key, ", ") + ")").Error; err != nil {
				return fmt.Errorf("rebuild the key of %s: %w", t.table, err)
			}
		}
	}

	if tx.Migrator().HasTable("hub_connections") && tx.Migrator().HasColumn("hub_connections", "tenant_id") {
		if err := repointConnections(tx, owners); err != nil {
			return err
		}
	}

	if tx.Migrator().HasTable("hub_history") {
		if err := tx.Exec("ALTER TABLE hub_history DROP COLUMN IF EXISTS legacy_id").Error; err != nil {
			return fmt.Errorf("drop legacy_id from hub_history: %w", err)
		}
	}
	if tx.Migrator().HasTable("hub_sessions") && tx.Migrator().HasColumn("hub_sessions", "tenant_id") {
		if err := tx.Exec("ALTER TABLE hub_sessions DROP COLUMN tenant_id").Error; err != nil {
			return fmt.Errorf("drop tenant_id from hub_sessions: %w", err)
		}
	}
	if tx.Migrator().HasTable("hub_teams") && tx.Migrator().HasColumn("hub_teams", "key") {
		if err := tx.Exec("ALTER TABLE hub_teams DROP COLUMN key").Error; err != nil {
			return fmt.Errorf("drop the team key: %w", err)
		}
	}

	return nil
}

func repointConnections(tx *gorm.DB, owners []legacyOwner) error {
	if err := tx.Exec("ALTER TABLE hub_connections ADD COLUMN IF NOT EXISTS organization_id bigint").Error; err != nil {
		return fmt.Errorf("add organization_id to hub_connections: %w", err)
	}
	for _, owner := range owners {
		if err := tx.Exec("UPDATE hub_connections SET organization_id = ? WHERE tenant_id = ?",
			owner.organization, owner.tenant).Error; err != nil {
			return fmt.Errorf("point hub_connections at its organization: %w", err)
		}
	}
	for _, statement := range []string{
		"ALTER TABLE hub_connections ALTER COLUMN organization_id SET NOT NULL",
		"ALTER TABLE hub_connections DROP COLUMN tenant_id",
		// Teams of one Organization each held their own copy of a connection,
		// and the Organization holds one. The newest copy stays.
		`DELETE FROM hub_connections a USING hub_connections b
		  WHERE a.organization_id = b.organization_id AND a.name = b.name AND a.field = b.field
		    AND (a.updated_at < b.updated_at OR (a.updated_at = b.updated_at AND a.ctid < b.ctid))`,
		"ALTER TABLE hub_connections ADD PRIMARY KEY (organization_id, name, field)",
	} {
		if err := tx.Exec(statement).Error; err != nil {
			return fmt.Errorf("rebuild hub_connections: %w", err)
		}
	}

	return nil
}

// dropLegacyTables removes what only the release before Organizations read.
func dropLegacyTables(tx *gorm.DB) error {
	for _, table := range []string{"hub_tenant_credentials", "hub_tenant_invites", "hub_imports"} {
		if err := tx.Exec("DROP TABLE IF EXISTS " + table).Error; err != nil {
			return fmt.Errorf("drop %s: %w", table, err)
		}
	}

	return nil
}
