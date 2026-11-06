package store_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/store"
)

// testKey is a deterministic 32 byte key. Tests assert on key ids, so the
// bytes have to be fixed rather than generated per run.
func testKey(fill byte) string {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = fill
	}

	return base64.StdEncoding.EncodeToString(raw)
}

func TestNewCipherRejectsUnusableKeys(t *testing.T) {
	tests := []struct {
		name    string
		primary string
	}{
		{"empty", ""},
		{"blank", "   "},
		{"not base64", "this is not base64 %%%"},
		{"too short", base64.StdEncoding.EncodeToString([]byte("sixteen bytes!!!"))},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.NewCipher(tc.primary)

			require.Error(t, err)
		})
	}
}

func TestSealAndOpenRoundTrip(t *testing.T) {
	cipher, err := store.NewCipher(testKey(1))
	require.NoError(t, err)

	sealed, err := cipher.Seal("row", "sn_live_abc123")
	require.NoError(t, err)

	got, err := cipher.Open("row", sealed)
	require.NoError(t, err)
	assert.Equal(t, "sn_live_abc123", got)
}

func TestSealHidesThePlaintext(t *testing.T) {
	cipher, err := store.NewCipher(testKey(2))
	require.NoError(t, err)

	sealed, err := cipher.Seal("row", "sn_live_abc123")
	require.NoError(t, err)

	assert.NotContains(t, sealed, "sn_live_abc123")
	assert.True(t, store.Sealed(sealed), "a sealed value has to be recognizable as one")
}

func TestSealIsNotDeterministic(t *testing.T) {
	cipher, err := store.NewCipher(testKey(3))
	require.NoError(t, err)

	first, err := cipher.Seal("row", "same value")
	require.NoError(t, err)
	second, err := cipher.Seal("row", "same value")
	require.NoError(t, err)

	assert.NotEqual(t, first, second, "a fresh nonce per seal is what stops equal values looking equal")
}

func TestSealRecordsTheKeyItUsed(t *testing.T) {
	cipher, err := store.NewCipher(testKey(4))
	require.NoError(t, err)

	sealed, err := cipher.Seal("row", "value")
	require.NoError(t, err)

	parts := strings.Split(sealed, ".")
	require.Len(t, parts, 3)
	assert.Equal(t, "v1", parts[0])
	assert.Equal(t, cipher.KeyID(), parts[1])
}

func TestOpenRefusesAnotherKey(t *testing.T) {
	writer, err := store.NewCipher(testKey(5))
	require.NoError(t, err)
	sealed, err := writer.Seal("row", "value")
	require.NoError(t, err)

	reader, err := store.NewCipher(testKey(6))
	require.NoError(t, err)

	_, err = reader.Open("row", sealed)

	require.Error(t, err, "a dump opened with the wrong key must not yield the value")
}

func TestOpenAcceptsARetiredKey(t *testing.T) {
	old, err := store.NewCipher(testKey(7))
	require.NoError(t, err)
	sealed, err := old.Seal("row", "value")
	require.NoError(t, err)

	rotated, err := store.NewCipher(testKey(8), testKey(7))
	require.NoError(t, err)

	got, err := rotated.Open("row", sealed)
	require.NoError(t, err)
	assert.Equal(t, "value", got)
	assert.NotEqual(t, old.KeyID(), rotated.KeyID(), "the primary key is the new one")
}

func TestSealAlwaysUsesThePrimaryKey(t *testing.T) {
	rotated, err := store.NewCipher(testKey(9), testKey(10))
	require.NoError(t, err)

	sealed, err := rotated.Seal("row", "value")
	require.NoError(t, err)

	assert.Contains(t, sealed, "."+rotated.KeyID()+".")
}

func TestOpenRejectsTamperedCiphertext(t *testing.T) {
	cipher, err := store.NewCipher(testKey(11))
	require.NoError(t, err)

	sealed, err := cipher.Seal("row", "value")
	require.NoError(t, err)

	body := []byte(sealed)
	body[len(body)-1] ^= 0xFF

	_, err = cipher.Open("row", string(body))

	require.Error(t, err)
}

func TestOpenRejectsMalformedValues(t *testing.T) {
	cipher, err := store.NewCipher(testKey(12))
	require.NoError(t, err)

	tests := []string{
		"",
		"plaintext",
		"v1.short",
		"v2." + cipher.KeyID() + ".AAAA",
		"v1." + cipher.KeyID() + ".not base64",
	}

	for _, sealed := range tests {
		t.Run(sealed, func(t *testing.T) {
			_, err := cipher.Open("row", sealed)

			require.Error(t, err)
		})
	}
}

func TestCipherErrorsNeverCarryThePlaintext(t *testing.T) {
	writer, err := store.NewCipher(testKey(13))
	require.NoError(t, err)
	sealed, err := writer.Seal("row", "sn_live_secret")
	require.NoError(t, err)

	reader, err := store.NewCipher(testKey(14))
	require.NoError(t, err)

	_, err = reader.Open("row", sealed)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "sn_live_secret")
	assert.NotContains(t, err.Error(), sealed)
}

func TestOpenRefusesAValueSealedForAnotherRow(t *testing.T) {
	cipher, err := store.NewCipher(testKey(15))
	require.NoError(t, err)

	sealed, err := cipher.Seal("hub_connections\x1facme\x1fservicenow\x1fSN_API_KEY", "sn_live_abc123")
	require.NoError(t, err)

	// The same key, the same field, another owner. Moving the row must not
	// hand the second owner the first one's credential.
	_, err = cipher.Open("hub_connections\x1fglobex\x1fservicenow\x1fSN_API_KEY", sealed)

	require.ErrorIs(t, err, store.ErrUnsealable)
}

func TestOpenRefusesAValueMovedBetweenFields(t *testing.T) {
	cipher, err := store.NewCipher(testKey(16))
	require.NoError(t, err)

	sealed, err := cipher.Seal("hub_connections\x1facme\x1fservicenow\x1fSN_API_KEY", "sn_live_abc123")
	require.NoError(t, err)

	_, err = cipher.Open("hub_connections\x1facme\x1fservicenow\x1fSN_CLIENT_TOKEN", sealed)

	require.ErrorIs(t, err, store.ErrUnsealable)
}

func TestBindingsCannotCollideAcrossTables(t *testing.T) {
	cipher, err := store.NewCipher(testKey(17))
	require.NoError(t, err)

	sealed, err := cipher.Seal("hub_clone_keys\x1facme\x1feks-prod", "identity")
	require.NoError(t, err)

	_, err = cipher.Open("hub_connections\x1facme\x1feks-prod", sealed)

	require.ErrorIs(t, err, store.ErrUnsealable)
}
