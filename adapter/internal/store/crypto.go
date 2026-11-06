package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// sealedVersion prefixes every stored value. It is a format marker, not a key
// version: a later format can be read alongside this one rather than needing
// every row rewritten first.
const sealedVersion = "v1"

// keyIDBytes is how much of the key digest names the key. Four bytes is eight
// hex characters, which is enough to pick one key out of the handful a
// deployment holds and short enough to read in a psql row.
const keyIDBytes = 4

// keySize is the AES-256 key length. Anything else is refused rather than
// stretched, so a truncated Secret value fails at boot instead of silently
// weakening every row.
const keySize = 32

// ErrUnsealable means a stored value could not be opened with the keys this
// process has. It is a distinct failure from the database being unreachable:
// the row is right there, and what is missing is the key, so the operator
// action is to restore it rather than to wait.
var ErrUnsealable = errors.New("a stored value could not be opened with the configured keys")

// errNoKey is what Open returns when no configured key wrote the value. It
// carries no part of the value, because the message reaches logs.
var errNoKey = errors.New("no configured encryption key matches this value")

// Cipher seals credentials the adapter has to replay downstream, so a database
// dump on its own does not yield a usable key. The keys live in a Kubernetes
// Secret and never in the database.
//
// Seal always uses the primary key. Open accepts the primary and any retired
// key, so a key can be replaced without rewriting every row first: rows keep
// naming the key that wrote them until something writes them again.
type Cipher struct {
	primary  aead
	byKeyID  map[string]aead
	primaryI string
}

// aead is one key, ready to use.
type aead struct {
	id  string
	gcm cipher.AEAD
}

// NewCipher builds a cipher from base64 encoded 32 byte keys. The first is the
// primary, the rest are accepted for reading only.
func NewCipher(primary string, retired ...string) (*Cipher, error) {
	first, err := loadKey(primary)
	if err != nil {
		return nil, err
	}

	c := &Cipher{primary: first, primaryI: first.id, byKeyID: map[string]aead{first.id: first}}
	for _, encoded := range retired {
		if strings.TrimSpace(encoded) == "" {
			continue
		}
		key, err := loadKey(encoded)
		if err != nil {
			return nil, err
		}
		if _, clash := c.byKeyID[key.id]; clash {
			continue
		}
		c.byKeyID[key.id] = key
	}

	return c, nil
}

// KeyID names the key Seal writes with. It is the first bytes of the key
// digest, so it is derived rather than tracked, and two deployments handed the
// same key agree on the name without being told it.
func (c *Cipher) KeyID() string {
	if c == nil {
		return ""
	}

	return c.primaryI
}

// Seal encrypts one value against the row it belongs to. The nonce is fresh per
// call, so two rows holding the same credential do not look alike.
//
// binding names that row: the owner, the table and the keys inside it. It is
// authenticated rather than encrypted, so a value cannot be moved to another
// row and still open. Without it every row in a shared database is sealed with
// the same key, and anyone who can write to the database could copy one
// Organization's credential into another's row and have it replayed
// downstream, which is the boundary the owner id exists to hold.
func (c *Cipher) Seal(binding, value string) (string, error) {
	if c == nil {
		return "", errors.New("no encryption key is configured")
	}

	nonce := make([]byte, c.primary.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("encryption nonce: %w", err)
	}

	body := c.primary.gcm.Seal(nonce, nonce, []byte(value), aad(c.primary.id, binding))

	return sealedVersion + "." + c.primary.id + "." + base64.RawStdEncoding.EncodeToString(body), nil
}

// aad is what a sealed value is bound to: the key that wrote it and the row it
// belongs to. The separator cannot appear in a key id, which is hex, so no two
// different bindings can produce the same bytes.
func aad(keyID, binding string) []byte {
	return []byte(keyID + "\x00" + binding)
}

// Open decrypts one value that was sealed against binding. Errors name the
// failure and never the value: they reach logs, and the point of the column is
// that its contents do not.
func (c *Cipher) Open(binding, sealed string) (string, error) {
	if c == nil {
		return "", errors.New("no encryption key is configured")
	}

	version, keyID, body, ok := splitSealed(sealed)
	if !ok || version != sealedVersion {
		return "", fmt.Errorf("%w: it is not in the sealed format", ErrUnsealable)
	}

	key, ok := c.byKeyID[keyID]
	if !ok {
		return "", fmt.Errorf("%w: %s, %s", ErrUnsealable, errNoKey, keyID)
	}

	raw, err := base64.RawStdEncoding.DecodeString(body)
	if err != nil {
		return "", fmt.Errorf("%w: it is not valid base64", ErrUnsealable)
	}

	size := key.gcm.NonceSize()
	if len(raw) < size {
		return "", fmt.Errorf("%w: it is too short to carry a nonce", ErrUnsealable)
	}

	out, err := key.gcm.Open(nil, raw[:size], raw[size:], aad(keyID, binding))
	if err != nil {
		// Either the key does not open it or it was sealed against another
		// row. The two are not told apart on purpose: the caller can act on
		// neither, and naming which would say something about the row to
		// whoever moved it.
		return "", fmt.Errorf("%w: it did not decrypt with the key and row it names", ErrUnsealable)
	}

	return string(out), nil
}

// Sealed reports whether a value is in the sealed format, without opening it.
// It answers the shape only, so a value that is sealed against another row or
// another key still reads as sealed.
func Sealed(value string) bool {
	version, keyID, body, ok := splitSealed(value)

	return ok && version == sealedVersion && keyID != "" && body != ""
}

func splitSealed(value string) (version, keyID, body string, ok bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return "", "", "", false
	}
	if parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}

	return parts[0], parts[1], parts[2], true
}

func loadKey(encoded string) (aead, error) {
	trimmed := strings.TrimSpace(encoded)
	if trimmed == "" {
		return aead{}, errors.New("no encryption key was configured")
	}

	raw, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		return aead{}, errors.New("the encryption key is not valid base64")
	}
	if len(raw) != keySize {
		return aead{}, fmt.Errorf("the encryption key has to be %d bytes, this one is %d", keySize, len(raw))
	}

	block, err := aes.NewCipher(raw)
	if err != nil {
		return aead{}, errors.New("the encryption key was refused by AES")
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return aead{}, errors.New("the encryption key could not be used for AES-GCM")
	}

	digest := sha256.Sum256(raw)

	return aead{id: hex.EncodeToString(digest[:keyIDBytes]), gcm: gcm}, nil
}
