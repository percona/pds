package store

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Errors the login flow distinguishes. A caller turns these into what the
// customer reads, so they have to stay tellable apart: a spent link and a link
// that never existed deserve different sentences.
var (
	// ErrLoginUnknown means nobody has created this login.
	ErrLoginUnknown = errors.New("no login is registered under that name")
	// ErrInviteUnknown means no invite was ever minted for that token.
	ErrInviteUnknown = errors.New("that invite link was never issued")
	// ErrInviteSpent means the link has already been claimed.
	ErrInviteSpent = errors.New("that invite link has already been used")
	// ErrInviteExpired means the link is past its expiry.
	ErrInviteExpired = errors.New("that invite link has expired")
	// ErrSessionUnknown means there is no live session under that id. An
	// expired or idled out session answers the same way, because to the caller
	// there is nothing to tell apart: either way they are signed out.
	ErrSessionUnknown = errors.New("that session is not signed in")
)

const (
	// sessionKeyID is the primary key of the single signing key row.
	sessionKeyID = 1

	// sessionKeySize is the HMAC key length in bytes.
	sessionKeySize = 32

	// sessionTouchInterval is how stale a session's last seen has to be before
	// a read writes it forward. Touching on every request would put a write in
	// front of every API call for an idle window measured in days.
	sessionTouchInterval = time.Minute
)

// Identity is the login state for every Organization on this deployment: who can
// sign in, which invite links are outstanding, and which sessions are live.
//
// It is deliberately not scoped to one Team the way HubState is. Signing in is
// how the Team gets decided, so a lookup by name has to be able to reach a row
// before any Team is known.
type Identity struct {
	db *gorm.DB
	// organization is the Organization a single-Organization deployment
	// serves. A login created here joins it, and empty or a star means a
	// login brings an Organization of its own.
	organization string
}

// Identity returns the login state.
func (s *Store) Identity() *Identity { return &Identity{db: s.db, organization: s.organization} }

func (i *Identity) scope(ctx context.Context) (*gorm.DB, error) {
	if i == nil || i.db == nil {
		return nil, errors.New("no database is configured for this login state")
	}

	return i.db.WithContext(ctx), nil
}

// EnsureLogin creates a password login with an Organization of its own, a
// default Team and an admin membership, and reports whether it did. The login
// starts with no password: the customer sets that by claiming an invite, so
// nothing here ever holds one Percona chose.
func (i *Identity) EnsureLogin(ctx context.Context, username string, now time.Time) (bool, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return false, ErrLoginUnknown
	}

	tx, err := i.scope(ctx)
	if err != nil {
		return false, err
	}

	var created bool
	err = tx.Transaction(func(tx *gorm.DB) error {
		// A login is global, so two creations of one name take turns.
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", organizationLockKey).Error; err != nil {
			return err
		}
		var held int64
		if err := tx.Model(&User{}).
			Where("issuer = ? AND subject = ?", passwordIssuer, username).Count(&held).Error; err != nil {
			return err
		}
		if held > 0 {
			return nil
		}
		created = true

		return i.addLoginTo(tx, username, now.UTC())
	})
	if err != nil {
		return false, fmt.Errorf("create login: %w", scrub(err))
	}

	return created, nil
}

// PasswordLogin is one password login as the sign in reads it.
type PasswordLogin struct {
	Username     string
	PasswordHash string
	// Failures counts consecutive wrong passwords and drives the sign in
	// throttle. It resets on a success. There is no lockout: freezing the login
	// would let anyone who knows a username lock that customer out.
	Failures  int
	CreatedAt time.Time
	UpdatedAt time.Time
}

func loginOf(u User) PasswordLogin {
	return PasswordLogin{
		Username: u.Subject, PasswordHash: u.PasswordHash, Failures: u.Failures,
		CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt,
	}
}

// Logins lists every password login, oldest first.
func (i *Identity) Logins(ctx context.Context) ([]PasswordLogin, error) {
	tx, err := i.scope(ctx)
	if err != nil {
		return nil, err
	}

	var rows []User
	if err := tx.Where("issuer = ?", passwordIssuer).Order("created_at, subject").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list logins: %w", scrub(err))
	}

	out := make([]PasswordLogin, 0, len(rows))
	for _, row := range rows {
		out = append(out, loginOf(row))
	}

	return out, nil
}

func passwordUser(tx *gorm.DB, username string) (User, error) {
	var row User
	err := tx.Where("issuer = ? AND subject = ?", passwordIssuer, strings.TrimSpace(username)).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return User{}, ErrLoginUnknown
	}
	if err != nil {
		return User{}, fmt.Errorf("read login: %w", scrub(err))
	}

	return row, nil
}

// PasswordLogin returns one login.
func (i *Identity) PasswordLogin(ctx context.Context, username string) (PasswordLogin, error) {
	tx, err := i.scope(ctx)
	if err != nil {
		return PasswordLogin{}, err
	}

	row, err := passwordUser(tx, username)
	if err != nil {
		return PasswordLogin{}, err
	}

	return loginOf(row), nil
}

// SetPassword replaces one login's hash and ends every session held under the
// old one. Both happen in one transaction, so there is no window where the new
// password is live and an old session still is.
func (i *Identity) SetPassword(ctx context.Context, username, passwordHash string, now time.Time) error {
	tx, err := i.scope(ctx)
	if err != nil {
		return err
	}

	return tx.Transaction(func(tx *gorm.DB) error {
		user, err := passwordUser(tx, username)
		if err != nil {
			return err
		}

		return replacePassword(tx, user.ID, passwordHash, now.UTC())
	})
}

// replacePassword sets a hash, clears the failure count and ends the User's
// sessions.
func replacePassword(tx *gorm.DB, userID uint, passwordHash string, now time.Time) error {
	if err := tx.Model(&User{}).Where("id = ?", userID).
		Updates(map[string]any{"password_hash": passwordHash, "failures": 0, "updated_at": now}).Error; err != nil {
		return fmt.Errorf("set password: %w", scrub(err))
	}

	return deleteSessionsFor(tx, userID)
}

// RecordFailure counts one wrong password and returns the new total. The count
// comes back from the row rather than from memory, so replicas agree on how
// hard to throttle the next attempt.
func (i *Identity) RecordFailure(ctx context.Context, username string, now time.Time) (int, error) {
	tx, err := i.scope(ctx)
	if err != nil {
		return 0, err
	}

	var failures []int
	if err := tx.Raw(
		`UPDATE hub_users
		    SET failures = failures + 1, updated_at = ?
		  WHERE issuer = ? AND subject = ?
		 RETURNING failures`,
		now.UTC(), passwordIssuer, strings.TrimSpace(username),
	).Scan(&failures).Error; err != nil {
		return 0, fmt.Errorf("record sign in failure: %w", scrub(err))
	}
	if len(failures) == 0 {
		return 0, ErrLoginUnknown
	}

	return failures[0], nil
}

// ClearFailures resets the throttle after a successful sign in.
func (i *Identity) ClearFailures(ctx context.Context, username string, now time.Time) error {
	tx, err := i.scope(ctx)
	if err != nil {
		return err
	}

	if err := tx.Model(&User{}).
		Where("issuer = ? AND subject = ? AND failures <> 0", passwordIssuer, strings.TrimSpace(username)).
		Updates(map[string]any{"failures": 0, "updated_at": now.UTC()}).Error; err != nil {
		return fmt.Errorf("clear sign in failures: %w", scrub(err))
	}

	return nil
}

// CreateInvite records one single-use link against the hash of its token, for
// an existing login.
func (i *Identity) CreateInvite(ctx context.Context, username, hash string, expiresAt, now time.Time) error {
	hash = strings.TrimSpace(hash)
	if strings.TrimSpace(username) == "" || hash == "" {
		return errors.New("an invite needs a login and a token")
	}

	tx, err := i.scope(ctx)
	if err != nil {
		return err
	}

	user, err := passwordUser(tx, username)
	if err != nil {
		return err
	}

	row := PasswordInvite{Hash: hash, UserID: user.ID, ExpiresAt: expiresAt.UTC(), CreatedAt: now.UTC()}
	if err := tx.Create(&row).Error; err != nil {
		return fmt.Errorf("create invite: %w", scrub(err))
	}

	return nil
}

// Invite reports which login a claimable link belongs to, without spending
// it. The claim page reads this, so opening the link twice has to be allowed.
func (i *Identity) Invite(ctx context.Context, hash string, now time.Time) (string, error) {
	tx, err := i.scope(ctx)
	if err != nil {
		return "", err
	}

	row, err := readInvite(tx, hash)
	if err != nil {
		return "", err
	}
	if err := claimable(row, now); err != nil {
		return "", err
	}

	return usernameOf(tx, row.UserID)
}

func usernameOf(tx *gorm.DB, userID uint) (string, error) {
	var user User
	if err := tx.Take(&user, userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrLoginUnknown
		}

		return "", fmt.Errorf("read login: %w", scrub(err))
	}

	return user.Subject, nil
}

// ClaimInvite spends the link, sets the login's password and ends every
// session it had, returning the login the link belonged to.
//
// The spend is a conditional update rather than a read followed by a write.
// Two browsers opening the same link both read an unclaimed row, so only the
// database can decide which one of them gets it.
func (i *Identity) ClaimInvite(ctx context.Context, hash, passwordHash string, now time.Time) (string, error) {
	hash = strings.TrimSpace(hash)
	tx, err := i.scope(ctx)
	if err != nil {
		return "", err
	}

	var username string
	err = tx.Transaction(func(tx *gorm.DB) error {
		spent := now.UTC()
		result := tx.Model(&PasswordInvite{}).
			Where("hash = ? AND claimed_at IS NULL AND expires_at > ?", hash, spent).
			Update("claimed_at", spent)
		if result.Error != nil {
			return fmt.Errorf("spend invite: %w", scrub(result.Error))
		}
		row, err := readInvite(tx, hash)
		if err != nil {
			return err
		}
		if result.RowsAffected == 0 {
			// Nothing was claimed, so the row says why: already spent, or out
			// of time.
			return claimable(row, now)
		}

		username, err = usernameOf(tx, row.UserID)
		if err != nil {
			return err
		}

		return replacePassword(tx, row.UserID, passwordHash, spent)
	})
	if err != nil {
		return "", err
	}

	return username, nil
}

// CreateSession records one signed-in browser.
func (i *Identity) CreateSession(ctx context.Context, session Session) error {
	if strings.TrimSpace(session.Hash) == "" || session.UserID == 0 {
		return errors.New("a session needs an id and a user")
	}

	tx, err := i.scope(ctx)
	if err != nil {
		return err
	}

	session.CreatedAt = session.CreatedAt.UTC()
	session.LastSeenAt = session.LastSeenAt.UTC()
	session.ExpiresAt = session.ExpiresAt.UTC()
	if err := tx.Create(&session).Error; err != nil {
		return fmt.Errorf("create session: %w", scrub(err))
	}

	return nil
}

// Session returns the live session under one id, or ErrSessionUnknown.
//
// A session that is past its absolute expiry, or has gone idle for longer than
// idle, is deleted here rather than merely refused: the row is dead either way
// and leaving it would keep answering the same question forever.
func (i *Identity) Session(
	ctx context.Context,
	hash string,
	now time.Time,
	idle time.Duration,
) (Session, error) {
	tx, err := i.scope(ctx)
	if err != nil {
		return Session{}, err
	}

	var row Session
	if err := tx.Where("hash = ?", strings.TrimSpace(hash)).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Session{}, ErrSessionUnknown
		}

		return Session{}, fmt.Errorf("read session: %w", scrub(err))
	}

	expired := !now.Before(row.ExpiresAt)
	idledOut := idle > 0 && !now.Before(row.LastSeenAt.Add(idle))
	if expired || idledOut {
		// The row is dead either way, so tidying it is best effort. Failing to
		// delete it is not a reason to answer anything other than signed out,
		// which is the true answer and the only one the caller can act on.
		_ = i.DeleteSession(ctx, row.Hash)

		return Session{}, ErrSessionUnknown
	}

	if now.Sub(row.LastSeenAt) >= sessionTouchInterval {
		seen := now.UTC()
		if err := tx.Model(&Session{}).
			Where("hash = ?", row.Hash).
			Update("last_seen_at", seen).Error; err != nil {
			return Session{}, fmt.Errorf("touch session: %w", scrub(err))
		}
		row.LastSeenAt = seen
	}

	return row, nil
}

// DeleteSession ends one session.
func (i *Identity) DeleteSession(ctx context.Context, hash string) error {
	tx, err := i.scope(ctx)
	if err != nil {
		return err
	}

	if err := tx.Where("hash = ?", strings.TrimSpace(hash)).
		Delete(&Session{}).Error; err != nil {
		return fmt.Errorf("delete session: %w", scrub(err))
	}

	return nil
}

// PurgeExpiredSessions drops the rows that are past their absolute expiry and
// returns how many went. Sign in calls it, so the table is tidied by use
// rather than by a timer nobody watches.
func (i *Identity) PurgeExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	tx, err := i.scope(ctx)
	if err != nil {
		return 0, err
	}

	result := tx.Where("expires_at <= ?", now.UTC()).Delete(&Session{})
	if result.Error != nil {
		return 0, fmt.Errorf("purge expired sessions: %w", scrub(result.Error))
	}

	return result.RowsAffected, nil
}

// SessionKey returns the key session cookies are signed with, making it on
// first use. Every replica reads the same row, so cookies survive a restart
// and a pod that was not the one to mint it.
func (i *Identity) SessionKey(ctx context.Context, now time.Time) ([]byte, error) {
	tx, err := i.scope(ctx)
	if err != nil {
		return nil, err
	}

	// Read first. Every authenticated request needs this key and it never
	// changes, so the ordinary path has to be one SELECT: inserting first would
	// put a write, and a dead tuple, in front of every API call.
	var stored SessionKey
	err = tx.Where("id = ?", sessionKeyID).Take(&stored).Error
	if err == nil {
		if len(stored.Key) == 0 {
			return nil, errors.New("the stored session key is empty")
		}

		return stored.Key, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("read session key: %w", scrub(err))
	}

	fresh := make([]byte, sessionKeySize)
	if _, err := rand.Read(fresh); err != nil {
		return nil, fmt.Errorf("generate session key: %w", err)
	}

	// Two replicas starting together both find no row and both try to mint
	// one, and the conflict clause makes the loser read the winner's key
	// rather than overwrite it.
	row := SessionKey{ID: sessionKeyID, Key: fresh, CreatedAt: now.UTC()}
	if err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoNothing: true,
	}).Create(&row).Error; err != nil {
		return nil, fmt.Errorf("create session key: %w", scrub(err))
	}

	if err := tx.Where("id = ?", sessionKeyID).Take(&stored).Error; err != nil {
		return nil, fmt.Errorf("read session key: %w", scrub(err))
	}
	if len(stored.Key) == 0 {
		return nil, errors.New("the stored session key is empty")
	}

	return stored.Key, nil
}

func deleteSessionsFor(tx *gorm.DB, userID uint) error {
	if err := tx.Where("user_id = ?", userID).Delete(&Session{}).Error; err != nil {
		return fmt.Errorf("delete sessions: %w", scrub(err))
	}

	return nil
}

func readInvite(tx *gorm.DB, hash string) (PasswordInvite, error) {
	var row PasswordInvite
	if err := tx.Where("hash = ?", strings.TrimSpace(hash)).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return PasswordInvite{}, ErrInviteUnknown
		}

		return PasswordInvite{}, fmt.Errorf("read invite: %w", scrub(err))
	}

	return row, nil
}

// claimable reports why a link cannot be used, or nil when it can. Spent is
// checked before expired so a claimed link that has also run out of time still
// reads as used rather than as never usable.
func claimable(row PasswordInvite, now time.Time) error {
	if row.ClaimedAt != nil {
		return ErrInviteSpent
	}
	if !now.Before(row.ExpiresAt) {
		return ErrInviteExpired
	}

	return nil
}
