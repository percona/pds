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

// Errors the OAuth service distinguishes.
var (
	// ErrOAuthClientUnknown means no client registered under that id.
	ErrOAuthClientUnknown = errors.New("no client is registered under that id")
	// ErrOAuthRequestUnknown means the parked request is gone, spent or expired.
	ErrOAuthRequestUnknown = errors.New("that authorization request is not waiting")
	// ErrOAuthCodeUnknown means the code was never issued or has expired.
	ErrOAuthCodeUnknown = errors.New("that authorization code is not valid")
	// ErrOAuthCodeUsed means the code was already exchanged. The row comes
	// back with the error so the caller can end the session the first
	// exchange made.
	ErrOAuthCodeUsed = errors.New("that authorization code was already used")
	// ErrOAuthTokenUnknown means no token has that hash.
	ErrOAuthTokenUnknown = errors.New("that token is not valid")
	// ErrOAuthTokenReused means a refresh token that was already exchanged
	// was presented again.
	ErrOAuthTokenReused = errors.New("that refresh token was already used")
	// ErrAgentSessionUnknown means the session is gone.
	ErrAgentSessionUnknown = errors.New("that agent session does not exist")
)

// OAuth is the authorization server's state: registered clients, parked
// requests, codes, agent sessions and their tokens.
type OAuth struct{ db *gorm.DB }

// OAuth returns the authorization server's state.
func (s *Store) OAuth() *OAuth { return &OAuth{db: s.db} }

func (o *OAuth) scope(ctx context.Context) (*gorm.DB, error) {
	if o == nil || o.db == nil {
		return nil, errors.New("no database is configured for the authorization server")
	}

	return o.db.WithContext(ctx), nil
}

// CreateClient stores a registration.
func (o *OAuth) CreateClient(ctx context.Context, client OAuthClient) error {
	tx, err := o.scope(ctx)
	if err != nil {
		return err
	}

	client.CreatedAt = client.CreatedAt.UTC()
	if err := tx.Create(&client).Error; err != nil {
		return fmt.Errorf("create client: %w", scrub(err))
	}

	return nil
}

// Client returns a registered client, or ErrOAuthClientUnknown.
func (o *OAuth) Client(ctx context.Context, id string) (OAuthClient, error) {
	tx, err := o.scope(ctx)
	if err != nil {
		return OAuthClient{}, err
	}

	var row OAuthClient
	if err := tx.Where("id = ?", id).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return OAuthClient{}, ErrOAuthClientUnknown
		}

		return OAuthClient{}, fmt.Errorf("read client: %w", scrub(err))
	}

	return row, nil
}

// UnusedClients counts the registrations that never produced a session.
func (o *OAuth) UnusedClients(ctx context.Context) (int64, error) {
	tx, err := o.scope(ctx)
	if err != nil {
		return 0, err
	}

	var count int64
	if err := tx.Model(&OAuthClient{}).Where("granted = ?", false).Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count clients: %w", scrub(err))
	}

	return count, nil
}

// MarkClientUsed records that a session was made for a client, which keeps its
// registration from being purged.
func (o *OAuth) MarkClientUsed(ctx context.Context, id string) error {
	tx, err := o.scope(ctx)
	if err != nil {
		return err
	}

	if err := tx.Model(&OAuthClient{}).Where("id = ?", id).Update("granted", true).Error; err != nil {
		return fmt.Errorf("mark client used: %w", scrub(err))
	}

	return nil
}

// PurgeClients drops the registrations made before cutoff that never produced
// a session.
func (o *OAuth) PurgeClients(ctx context.Context, cutoff time.Time) error {
	tx, err := o.scope(ctx)
	if err != nil {
		return err
	}

	if err := tx.Where("granted = ? AND created_at < ?", false, cutoff.UTC()).Delete(&OAuthClient{}).Error; err != nil {
		return fmt.Errorf("purge clients: %w", scrub(err))
	}

	return nil
}

// CreateRequest parks a validated authorization request.
func (o *OAuth) CreateRequest(ctx context.Context, row OAuthRequest) error {
	tx, err := o.scope(ctx)
	if err != nil {
		return err
	}

	row.CreatedAt, row.ExpiresAt = row.CreatedAt.UTC(), row.ExpiresAt.UTC()
	if err := tx.Create(&row).Error; err != nil {
		return fmt.Errorf("park request: %w", scrub(err))
	}

	return nil
}

// Request reads a parked request without consuming it.
func (o *OAuth) Request(ctx context.Context, hash string, now time.Time) (OAuthRequest, error) {
	tx, err := o.scope(ctx)
	if err != nil {
		return OAuthRequest{}, err
	}

	var row OAuthRequest
	if err := tx.Where("hash = ? AND expires_at > ?", hash, now.UTC()).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return OAuthRequest{}, ErrOAuthRequestUnknown
		}

		return OAuthRequest{}, fmt.Errorf("read request: %w", scrub(err))
	}

	return row, nil
}

// TakeRequest removes a parked request and returns it. Two takes of the same
// request cannot both succeed, so a double submit makes one code.
func (o *OAuth) TakeRequest(ctx context.Context, hash string, now time.Time) (OAuthRequest, error) {
	tx, err := o.scope(ctx)
	if err != nil {
		return OAuthRequest{}, err
	}

	var rows []OAuthRequest
	if err := tx.Clauses(clause.Returning{}).
		Where("hash = ? AND expires_at > ?", hash, now.UTC()).
		Delete(&rows).Error; err != nil {
		return OAuthRequest{}, fmt.Errorf("take request: %w", scrub(err))
	}
	if len(rows) == 0 {
		return OAuthRequest{}, ErrOAuthRequestUnknown
	}

	return rows[0], nil
}

// CreateCode stores an authorization code.
func (o *OAuth) CreateCode(ctx context.Context, code OAuthCode) error {
	tx, err := o.scope(ctx)
	if err != nil {
		return err
	}

	code.CreatedAt, code.ExpiresAt = code.CreatedAt.UTC(), code.ExpiresAt.UTC()
	if err := tx.Create(&code).Error; err != nil {
		return fmt.Errorf("create code: %w", scrub(err))
	}

	return nil
}

// UseCode marks a code used and returns it. It is one UPDATE that matches only
// a code not used yet, so of any number of simultaneous requests exactly one
// gets the code. A code used before answers ErrOAuthCodeUsed with the row, and
// one that is unknown or past its expiry answers ErrOAuthCodeUnknown.
func (o *OAuth) UseCode(ctx context.Context, hash string, now time.Time) (OAuthCode, error) {
	tx, err := o.scope(ctx)
	if err != nil {
		return OAuthCode{}, err
	}

	var won []OAuthCode
	if err := tx.Raw(
		"UPDATE oauth_codes SET used_at = ? WHERE hash = ? AND used_at IS NULL AND expires_at > ? RETURNING *",
		now.UTC(), hash, now.UTC(),
	).Scan(&won).Error; err != nil {
		return OAuthCode{}, fmt.Errorf("use code: %w", scrub(err))
	}
	if len(won) == 1 {
		return won[0], nil
	}

	var row OAuthCode
	if err := tx.Where("hash = ? AND used_at IS NOT NULL AND expires_at > ?", hash, now.UTC()).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return OAuthCode{}, ErrOAuthCodeUnknown
		}

		return OAuthCode{}, fmt.Errorf("read code: %w", scrub(err))
	}

	return row, ErrOAuthCodeUsed
}

// CreateAgentSession stores a session and its first tokens in one step, and
// when codeHash is set, records on the code which session it bought.
func (o *OAuth) CreateAgentSession(ctx context.Context, session AgentSession, tokens []OAuthToken, codeHash string) error {
	tx, err := o.scope(ctx)
	if err != nil {
		return err
	}

	session.CreatedAt, session.LastUsedAt, session.ExpiresAt =
		session.CreatedAt.UTC(), session.LastUsedAt.UTC(), session.ExpiresAt.UTC()

	err = tx.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&session).Error; err != nil {
			return err
		}
		if err := createTokens(tx, tokens); err != nil {
			return err
		}
		if codeHash != "" {
			if err := tx.Model(&OAuthCode{}).Where("hash = ?", codeHash).Update("session_id", session.ID).Error; err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("create agent session: %w", scrub(err))
	}

	return nil
}

func createTokens(tx *gorm.DB, tokens []OAuthToken) error {
	for i := range tokens {
		tokens[i].ExpiresAt = tokens[i].ExpiresAt.UTC()
	}

	return tx.Create(&tokens).Error
}

// TokenSession returns the token under a hash and the session it belongs to.
// It does not judge either one's expiry: the caller holds the clock and the
// idle rule.
func (o *OAuth) TokenSession(ctx context.Context, hash string) (OAuthToken, AgentSession, error) {
	tx, err := o.scope(ctx)
	if err != nil {
		return OAuthToken{}, AgentSession{}, err
	}

	var token OAuthToken
	if err := tx.Where("hash = ?", strings.TrimSpace(hash)).Take(&token).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return OAuthToken{}, AgentSession{}, ErrOAuthTokenUnknown
		}

		return OAuthToken{}, AgentSession{}, fmt.Errorf("read token: %w", scrub(err))
	}

	var session AgentSession
	if err := tx.Where("id = ?", token.SessionID).Take(&session).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return OAuthToken{}, AgentSession{}, ErrOAuthTokenUnknown
		}

		return OAuthToken{}, AgentSession{}, fmt.Errorf("read agent session: %w", scrub(err))
	}

	return token, session, nil
}

// RotateRefresh retires a refresh token and stores the tokens that replace
// it, and counts the refresh as use of the session. The session row is locked
// first, so a refresh racing a revoke either finishes before it or finds the
// session gone. A refresh token already retired answers ErrOAuthTokenReused and
// changes nothing.
func (o *OAuth) RotateRefresh(ctx context.Context, hash string, now time.Time, tokens []OAuthToken) error {
	tx, err := o.scope(ctx)
	if err != nil {
		return err
	}

	err = tx.Transaction(func(tx *gorm.DB) error {
		var old OAuthToken
		if err := tx.Where("hash = ? AND kind = ?", hash, TokenRefresh).Take(&old).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrOAuthTokenUnknown
			}

			return err
		}

		var session AgentSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", old.SessionID).Take(&session).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAgentSessionUnknown
			}

			return err
		}

		result := tx.Model(&OAuthToken{}).
			Where("hash = ? AND rotated_at IS NULL", hash).
			Update("rotated_at", now.UTC())
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrOAuthTokenReused
		}

		if err := createTokens(tx, tokens); err != nil {
			return err
		}
		if err := tx.Model(&AgentSession{}).Where("id = ?", session.ID).
			Update("last_used_at", now.UTC()).Error; err != nil {
			return err
		}

		return tx.Where("session_id = ? AND expires_at <= ?", session.ID, now.UTC()).Delete(&OAuthToken{}).Error
	})
	if err != nil {
		if errors.Is(err, ErrOAuthTokenUnknown) || errors.Is(err, ErrOAuthTokenReused) || errors.Is(err, ErrAgentSessionUnknown) {
			return err
		}

		return fmt.Errorf("rotate refresh token: %w", scrub(err))
	}

	return nil
}

// TouchAgentSession records that a session was used.
func (o *OAuth) TouchAgentSession(ctx context.Context, id string, now time.Time) error {
	tx, err := o.scope(ctx)
	if err != nil {
		return err
	}

	if err := tx.Model(&AgentSession{}).Where("id = ?", id).Update("last_used_at", now.UTC()).Error; err != nil {
		return fmt.Errorf("touch agent session: %w", scrub(err))
	}

	return nil
}

// AgentSessions lists the sessions of an Organization that have not passed
// their expiry, newest use first. A userID of zero lists every person's, and
// otherwise only that person's. The caller applies the idle rule, which needs
// its own configuration.
func (o *OAuth) AgentSessions(ctx context.Context, organizationID, userID uint, now time.Time) ([]AgentSession, error) {
	tx, err := o.scope(ctx)
	if err != nil {
		return nil, err
	}

	query := tx.Where("organization_id = ? AND expires_at > ?", organizationID, now.UTC())
	if userID != 0 {
		query = query.Where("user_id = ?", userID)
	}

	var sessions []AgentSession
	if err := query.Order("last_used_at DESC, id").Find(&sessions).Error; err != nil {
		return nil, fmt.Errorf("list agent sessions: %w", scrub(err))
	}

	return sessions, nil
}

// DeleteAgentSession ends a session and every token it holds. Deleting one that
// is already gone is not an error.
func (o *OAuth) DeleteAgentSession(ctx context.Context, id string) error {
	tx, err := o.scope(ctx)
	if err != nil {
		return err
	}

	err = tx.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("session_id = ?", id).Delete(&OAuthToken{}).Error; err != nil {
			return err
		}

		return tx.Where("id = ?", id).Delete(&AgentSession{}).Error
	})
	if err != nil {
		return fmt.Errorf("delete agent session: %w", scrub(err))
	}

	return nil
}

// PurgeOAuth drops what has expired: sessions and their tokens, loose tokens,
// parked requests and codes. Sign in, registration and exchange call it, so
// the tables are tidied by use.
func (o *OAuth) PurgeOAuth(ctx context.Context, now time.Time) error {
	tx, err := o.scope(ctx)
	if err != nil {
		return err
	}

	at := now.UTC()
	err = tx.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("session_id IN (?)",
			tx.Model(&AgentSession{}).Select("id").Where("expires_at <= ?", at)).
			Delete(&OAuthToken{}).Error; err != nil {
			return err
		}
		if err := tx.Where("expires_at <= ?", at).Delete(&AgentSession{}).Error; err != nil {
			return err
		}
		if err := tx.Where("expires_at <= ?", at).Delete(&OAuthToken{}).Error; err != nil {
			return err
		}
		if err := tx.Where("expires_at <= ?", at).Delete(&OAuthRequest{}).Error; err != nil {
			return err
		}

		return tx.Where("expires_at <= ?", at).Delete(&OAuthCode{}).Error
	})
	if err != nil {
		return fmt.Errorf("purge authorization state: %w", scrub(err))
	}

	return nil
}
