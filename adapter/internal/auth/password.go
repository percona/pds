package auth

import (
	"errors"
	"fmt"
	"regexp"

	"golang.org/x/crypto/bcrypt"
)

const (
	// DefaultPasswordCost is the bcrypt work factor for a stored password.
	// Twelve costs a couple of hundred milliseconds on the hardware this runs
	// on, which is invisible on the one sign in a session needs and expensive
	// enough to make an offline guess against a stolen hash slow.
	//
	// It is a parameter rather than a constant inside HashPassword because it
	// is the one knob worth turning: faster hardware wants a higher cost, and
	// a test suite that hashes hundreds of times wants the floor.
	DefaultPasswordCost = 12

	// MinPasswordLength is the shortest password a customer may choose. The
	// customer picks it through the invite link, so length is the only rule
	// worth enforcing: composition rules push people towards shorter
	// passwords they reuse.
	MinPasswordLength = 12

	// MaxPasswordLength is bcrypt's own limit. It reads the first 72 bytes and
	// ignores the rest, so a longer password would be silently truncated and a
	// different password would then open the account.
	MaxPasswordLength = 72
)

const (
	// MinUsernameLength keeps a username long enough to read.
	MinUsernameLength = 3
	// MaxUsernameLength is the DNS label limit, so a username stays usable
	// wherever a Kubernetes name is needed.
	MaxUsernameLength = 63
)

// usernamePattern is a DNS label that starts with a letter: lower case
// letters, digits and hyphens, no hyphen at either end. It is the same shape the
// rest of the product validates names against.
var usernamePattern = regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?$`)

// HashPassword returns the bcrypt hash to store, at the given cost. A cost of
// zero or less uses DefaultPasswordCost.
//
// It validates first, so no caller can store a hash of a password bcrypt would
// have truncated.
func HashPassword(password string, cost int) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	if cost <= 0 {
		cost = DefaultPasswordCost
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), cost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}

	return string(hash), nil
}

// PasswordMatches reports whether the password is the one behind the hash. The
// cost is read from the hash, so a password stored at an older cost keeps
// working after the default is raised.
//
// An empty hash never matches. That is the state a login sits in between
// Percona creating it and the customer claiming the invite, and it has to be
// unusable rather than open to an empty password.
func PasswordMatches(hash, password string) bool {
	if hash == "" {
		return false
	}

	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// ValidatePassword reports whether a customer chosen password is usable.
func ValidatePassword(password string) error {
	if len(password) < MinPasswordLength {
		return fmt.Errorf("a password needs at least %d characters", MinPasswordLength)
	}
	if len(password) > MaxPasswordLength {
		return fmt.Errorf("a password can be at most %d characters", MaxPasswordLength)
	}

	return nil
}

// ValidateUsername reports whether a name can be a login.
func ValidateUsername(id string) error {
	if id == "" {
		return errors.New("a username is required")
	}
	if len(id) < MinUsernameLength || len(id) > MaxUsernameLength {
		return fmt.Errorf("a username needs between %d and %d characters",
			MinUsernameLength, MaxUsernameLength)
	}
	if !usernamePattern.MatchString(id) {
		return errors.New(
			"a username starts with a letter and holds only lower case letters, digits and hyphens")
	}

	return nil
}
