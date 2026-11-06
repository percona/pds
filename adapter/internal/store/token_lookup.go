package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Errors the unscoped token lookup distinguishes. A caller turns unknown into
// 401, and a collision must not pick a team: the primary key is
// (team_id, hash), so two rows for one hash is not a normal case.
var (
	// ErrTokenUnknown means no hub_tokens row carries this hash.
	ErrTokenUnknown = errors.New("that token hash was never issued")
	// ErrTokenCollision means more than one team stored the same hash.
	ErrTokenCollision = errors.New("that token hash belongs to more than one team")
)

// TokenTeam returns the Team that minted this token hash.
//
// The read is deliberately not team scoped. Agent routes have no customer
// session, and HubState.scope refuses a blank team, so the only way to
// recover who minted the bearer is a tokens-table lookup by hash alone.
func (s *Store) TokenTeam(ctx context.Context, hash string) (uint, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("no database is configured for this token lookup")
	}
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return 0, ErrTokenUnknown
	}

	var rows []HubToken
	if err := s.db.WithContext(ctx).Where("hash = ?", hash).Limit(2).Find(&rows).Error; err != nil {
		return 0, fmt.Errorf("read token team: %w", scrub(err))
	}
	switch len(rows) {
	case 0:
		return 0, ErrTokenUnknown
	case 1:
		return rows[0].TeamID, nil
	default:
		return 0, ErrTokenCollision
	}
}
