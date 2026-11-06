package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHashTokenIsSHA256Hex(t *testing.T) {
	sum := sha256.Sum256([]byte("jnt_example"))
	assert.Equal(t, hex.EncodeToString(sum[:]), HashToken("jnt_example"))
	assert.Equal(t, HashToken("jnt_example"), hashToken("jnt_example"))
}
