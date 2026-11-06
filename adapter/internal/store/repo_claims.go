package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errNoRepoClaimDatabase = errors.New("no database is configured for repository claims")

// ClaimRepo gives a repository name to an owner. It reports won when the owner
// holds the name afterwards, and fresh when this call is what gave it to them,
// so a caller can tell a name it already used from one it has just taken. The
// insert is the arbiter: of several owners racing for a name exactly one wins.
func (s *Store) ClaimRepo(ctx context.Context, repo, owner string) (won, fresh bool, err error) {
	if s == nil || s.db == nil {
		return false, false, errNoRepoClaimDatabase
	}
	repo = strings.TrimSpace(repo)
	owner = strings.TrimSpace(owner)
	if repo == "" || owner == "" {
		return false, false, errors.New("a repository claim needs a name and an owner")
	}

	tx := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
		Create(&HubRepoClaim{Name: repo, Owner: owner, CreatedAt: time.Now().UTC()})
	if tx.Error != nil {
		return false, false, fmt.Errorf("claim repository: %w", scrub(tx.Error))
	}
	if tx.RowsAffected == 1 {
		return true, true, nil
	}

	var held HubRepoClaim
	if err := s.db.WithContext(ctx).Where("name = ?", repo).Take(&held).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Released between the insert and the read: the caller claims again.
			return false, false, nil
		}

		return false, false, fmt.Errorf("read repository claim: %w", scrub(err))
	}

	return held.Owner == owner, false, nil
}

// ReleaseRepo gives a name back. Only its owner can.
func (s *Store) ReleaseRepo(ctx context.Context, repo, owner string) error {
	if s == nil || s.db == nil {
		return errNoRepoClaimDatabase
	}
	err := s.db.WithContext(ctx).Where("name = ? AND owner = ?", strings.TrimSpace(repo), strings.TrimSpace(owner)).
		Delete(&HubRepoClaim{}).Error
	if err != nil {
		return fmt.Errorf("release repository: %w", scrub(err))
	}

	return nil
}
