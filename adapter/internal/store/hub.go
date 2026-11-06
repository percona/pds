package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// errNoTeam is what every read and write returns without one. A row that
// reached the database under a blank team would be visible to whichever team
// was next handed a blank one, which is the opposite of the point.
var errNoTeam = errors.New("no team was configured for this hub state")

// HubState is the hub's own state, scoped to one Team.
//
// Every method filters on the Team and every write carries it, so two
// Teams hold two sets of rows rather than sharing one Kubernetes object.
// Connections are the exception: they belong to the Organization the Team is
// in, and every Team of it reads them.
// The documents are the same JSON the ConfigMaps held, so the shapes the hub
// marshals did not have to change to move here.
type HubState struct {
	db     *gorm.DB
	team   uint
	cipher *Cipher
}

// Hub returns the state for one Team. The cipher seals the values the
// adapter has to replay downstream and may be nil only where none are read.
func (s *Store) Hub(team uint, cipher *Cipher) *HubState {
	return &HubState{db: s.db, team: team, cipher: cipher}
}

func (h *HubState) scope(ctx context.Context) (*gorm.DB, error) {
	if h == nil || h.db == nil {
		return nil, errors.New("no database is configured for this hub state")
	}
	if h.team == 0 {
		return nil, errNoTeam
	}

	return h.db.WithContext(ctx).Where("team_id = ?", h.team), nil
}

// organization is the Organization this Team belongs to, which owns the
// connections every one of its Teams reads.
func (h *HubState) organization(ctx context.Context) (uint, error) {
	if h == nil || h.db == nil {
		return 0, errors.New("no database is configured for this hub state")
	}
	if h.team == 0 {
		return 0, errNoTeam
	}

	var team Team
	if err := h.db.WithContext(ctx).Select("organization_id").Take(&team, h.team).Error; err != nil {
		return 0, fmt.Errorf("read the team's organization: %w", scrub(err))
	}

	return team.OrganizationID, nil
}

// Environments returns every environment document this team holds, keyed by name.
func (h *HubState) Environments(ctx context.Context) (map[string]string, error) {
	tx, err := h.scope(ctx)
	if err != nil {
		return nil, err
	}

	var rows []HubEnvironment
	if err := tx.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read hub environments: %w", scrub(err))
	}

	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.Name] = row.Document
	}

	return out, nil
}

// SaveEnvironment replaces one environment document.
func (h *HubState) SaveEnvironment(ctx context.Context, name, document string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("an environment row needs a name")
	}
	if err := h.upsert(ctx, &HubEnvironment{
		TeamID: h.team, Name: name, Document: document, UpdatedAt: time.Now().UTC(),
	}, []string{"team_id", "name"}, "document", "updated_at"); err != nil {
		return fmt.Errorf("save hub environment: %w", err)
	}

	return nil
}

// RemoveEnvironment drops one environment.
func (h *HubState) RemoveEnvironment(ctx context.Context, name string) error {
	tx, err := h.scope(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(name) == "" {
		return nil
	}

	if err := tx.Where("name = ?", name).Delete(&HubEnvironment{}).Error; err != nil {
		return fmt.Errorf("remove hub environment: %w", scrub(err))
	}

	return nil
}

// Tokens returns every grant this team holds, keyed by token hash.
func (h *HubState) Tokens(ctx context.Context) (map[string]string, error) {
	tx, err := h.scope(ctx)
	if err != nil {
		return nil, err
	}

	var rows []HubToken
	if err := tx.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read hub tokens: %w", scrub(err))
	}

	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.Hash] = row.Document
	}

	return out, nil
}

// SaveToken writes one grant against its hash.
func (h *HubState) SaveToken(ctx context.Context, hash, document string) error {
	if strings.TrimSpace(hash) == "" {
		return errors.New("a token row needs a hash")
	}
	if err := h.upsert(ctx, &HubToken{
		TeamID: h.team, Hash: hash, Document: document, UpdatedAt: time.Now().UTC(),
	}, []string{"team_id", "hash"}, "document", "updated_at"); err != nil {
		return fmt.Errorf("save hub token: %w", err)
	}

	return nil
}

// RemoveTokens drops the named hashes and leaves the rest. Revoking is the
// other half of minting: without it a decommissioned environment's token stays
// valid for as long as the row exists.
func (h *HubState) RemoveTokens(ctx context.Context, hashes []string) error {
	if len(hashes) == 0 {
		return nil
	}
	tx, err := h.scope(ctx)
	if err != nil {
		return err
	}

	if err := tx.Where("hash IN ?", hashes).Delete(&HubToken{}).Error; err != nil {
		return fmt.Errorf("remove hub tokens: %w", scrub(err))
	}

	return nil
}

// Work returns every environment's outbound queue, keyed by environment.
func (h *HubState) Work(ctx context.Context) (map[string]string, error) {
	tx, err := h.scope(ctx)
	if err != nil {
		return nil, err
	}

	var rows []HubWork
	if err := tx.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read hub work queue: %w", scrub(err))
	}

	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.Environment] = row.Document
	}

	return out, nil
}

// SaveWork replaces one environment's queue.
func (h *HubState) SaveWork(ctx context.Context, environment, document string) error {
	if strings.TrimSpace(environment) == "" {
		return errors.New("a work row needs an environment")
	}
	if err := h.upsert(ctx, &HubWork{
		TeamID: h.team, Environment: environment, Document: document, UpdatedAt: time.Now().UTC(),
	}, []string{"team_id", "environment"}, "document", "updated_at"); err != nil {
		return fmt.Errorf("save hub work queue: %w", err)
	}

	return nil
}

// SupportAccess returns every environment's support access document this team
// holds, keyed by environment.
func (h *HubState) SupportAccess(ctx context.Context) (map[string]string, error) {
	tx, err := h.scope(ctx)
	if err != nil {
		return nil, err
	}

	var rows []HubSupportAccess
	if err := tx.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read hub support access: %w", scrub(err))
	}

	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.Environment] = row.Document
	}

	return out, nil
}

// AllSupportAccess returns the support access document of every environment of
// every Team. It is deliberately not Team scoped: the private network is shared,
// so telling a stray device from a live one takes every Team's windows.
func (s *Store) AllSupportAccess(ctx context.Context) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("no database is configured for this support access read")
	}

	var rows []HubSupportAccess
	if err := s.db.WithContext(ctx).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read every support access record: %w", scrub(err))
	}

	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Document)
	}

	return out, nil
}

// SaveSupportAccess replaces one environment's support access document.
func (h *HubState) SaveSupportAccess(ctx context.Context, environment, document string) error {
	if strings.TrimSpace(environment) == "" {
		return errors.New("a support access row needs an environment")
	}
	if err := h.upsert(ctx, &HubSupportAccess{
		TeamID: h.team, Environment: environment, Document: document, UpdatedAt: time.Now().UTC(),
	}, []string{"team_id", "environment"}, "document", "updated_at"); err != nil {
		return fmt.Errorf("save hub support access: %w", err)
	}

	return nil
}

// RemoveSupportAccess drops one environment's support access document.
func (h *HubState) RemoveSupportAccess(ctx context.Context, environment string) error {
	tx, err := h.scope(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(environment) == "" {
		return nil
	}

	if err := tx.Where("environment = ?", environment).Delete(&HubSupportAccess{}).Error; err != nil {
		return fmt.Errorf("remove hub support access: %w", scrub(err))
	}

	return nil
}

// CloneKeys returns every clone identity this team holds, opened.
func (h *HubState) CloneKeys(ctx context.Context) (map[string]string, error) {
	tx, err := h.scope(ctx)
	if err != nil {
		return nil, err
	}

	var rows []HubCloneKey
	if err := tx.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read hub clone keys: %w", scrub(err))
	}

	out := make(map[string]string, len(rows))
	for _, row := range rows {
		opened, err := h.open(cloneKeyBinding(h.team, row.Environment), row.Sealed)
		if err != nil {
			return nil, fmt.Errorf("read hub clone key for %s: %w", row.Environment, err)
		}
		out[row.Environment] = opened
	}

	return out, nil
}

// SaveCloneKey seals and writes one clone identity.
func (h *HubState) SaveCloneKey(ctx context.Context, environment, document string) error {
	if strings.TrimSpace(environment) == "" {
		return errors.New("a clone key row needs an environment")
	}

	sealed, err := h.seal(cloneKeyBinding(h.team, environment), document)
	if err != nil {
		return err
	}
	if err := h.upsert(ctx, &HubCloneKey{
		TeamID: h.team, Environment: environment, Sealed: sealed, UpdatedAt: time.Now().UTC(),
	}, []string{"team_id", "environment"}, "sealed", "updated_at"); err != nil {
		return fmt.Errorf("save hub clone key: %w", err)
	}

	return nil
}

// RemoveCloneKey drops one clone identity.
func (h *HubState) RemoveCloneKey(ctx context.Context, environment string) error {
	tx, err := h.scope(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(environment) == "" {
		return nil
	}

	if err := tx.Where("environment = ?", environment).Delete(&HubCloneKey{}).Error; err != nil {
		return fmt.Errorf("remove hub clone key: %w", scrub(err))
	}

	return nil
}

// Connection returns one credential's fields, opened. A credential with no
// rows is an empty map rather than an error: that is a deployment that has
// never been given one.
func (h *HubState) Connection(ctx context.Context, name string) (map[string]string, error) {
	organization, err := h.organization(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("a connection needs a name")
	}

	var rows []HubConnection
	if err := h.db.WithContext(ctx).Where("organization_id = ? AND name = ?", organization, name).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read connection: %w", scrub(err))
	}

	out := make(map[string]string, len(rows))
	for _, row := range rows {
		opened, err := h.open(connectionBinding(organization, row.Name, row.Field), row.Sealed)
		if err != nil {
			// A value that will not open is reported rather than dropped. An
			// empty map would read as "no credential configured" and send the
			// UI to a paste form over a credential that is still there.
			return nil, fmt.Errorf("read connection field %s: %w", row.Field, err)
		}
		out[row.Field] = opened
	}

	return out, nil
}

// SaveConnection replaces one credential's fields. Fields the caller stopped
// sending are removed, so a ServiceNow URL cleared in the UI does not survive
// as a stale row beside the key that replaced it.
func (h *HubState) SaveConnection(ctx context.Context, name string, values map[string]string) error {
	organization, err := h.organization(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("a connection needs a name")
	}

	rows := make([]HubConnection, 0, len(values))
	keep := make([]string, 0, len(values))
	now := time.Now().UTC()
	for field, value := range values {
		if strings.TrimSpace(field) == "" {
			continue
		}
		sealed, err := h.seal(connectionBinding(organization, name, field), value)
		if err != nil {
			return err
		}
		rows = append(rows, HubConnection{
			OrganizationID: organization, Name: name, Field: field, Sealed: sealed, UpdatedAt: now,
		})
		keep = append(keep, field)
	}

	// One transaction, so a credential is never half replaced: a reader either
	// sees the fields that were there before or the whole new set.
	err = h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		drop := tx.Where("organization_id = ? AND name = ?", organization, name)
		if len(keep) > 0 {
			drop = drop.Where("field NOT IN ?", keep)
		}
		if err := drop.Delete(&HubConnection{}).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}

		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "organization_id"}, {Name: "name"}, {Name: "field"}},
			DoUpdates: clause.AssignmentColumns([]string{"sealed", "updated_at"}),
		}).Create(&rows).Error
	})
	if err != nil {
		return fmt.Errorf("save connection: %w", scrub(err))
	}

	return nil
}

// RemoveConnection drops every field of one credential.
func (h *HubState) RemoveConnection(ctx context.Context, name string) error {
	organization, err := h.organization(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(name) == "" {
		return nil
	}

	if err := h.db.WithContext(ctx).Where("organization_id = ? AND name = ?", organization, name).
		Delete(&HubConnection{}).Error; err != nil {
		return fmt.Errorf("remove connection: %w", scrub(err))
	}

	return nil
}

func (h *HubState) upsert(ctx context.Context, row any, keys []string, updates ...string) error {
	if h == nil || h.db == nil {
		return errors.New("no database is configured for this hub state")
	}
	if h.team == 0 {
		return errNoTeam
	}

	columns := make([]clause.Column, 0, len(keys))
	for _, key := range keys {
		columns = append(columns, clause.Column{Name: key})
	}

	if err := h.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   columns,
		DoUpdates: clause.AssignmentColumns(updates),
	}).Create(row).Error; err != nil {
		return scrub(err)
	}

	return nil
}

// connectionBinding and cloneKeyBinding name the row a sealed value belongs to,
// so a value copied into another Organization's or Team's row, or another field
// of the same one, will not open there. The table name is part of it because an
// owner id and an environment name alone would be the same in both.
func connectionBinding(organization uint, name, field string) string {
	return "hub_connections\x1f" + strconv.FormatUint(uint64(organization), 10) + "\x1f" + name + "\x1f" + field
}

func cloneKeyBinding(team uint, environment string) string {
	return "hub_clone_keys\x1f" + strconv.FormatUint(uint64(team), 10) + "\x1f" + environment
}

func (h *HubState) seal(binding, value string) (string, error) {
	if h.cipher == nil {
		return "", errors.New("no encryption key is configured, so this value cannot be stored")
	}

	return h.cipher.Seal(binding, value)
}

func (h *HubState) open(binding, sealed string) (string, error) {
	if h.cipher == nil {
		return "", errors.New("no encryption key is configured, so this value cannot be read")
	}

	return h.cipher.Open(binding, sealed)
}
