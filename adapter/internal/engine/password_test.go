package engine_test

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/engine"
)

func TestRandomPasswordIsAlphanumericAndNotReused(t *testing.T) {
	first, err := engine.RandomPassword()
	require.NoError(t, err)
	second, err := engine.RandomPassword()
	require.NoError(t, err)

	assert.Regexp(t, regexp.MustCompile(`^[A-Za-z0-9]{24}$`), first)
	assert.NotEqual(t, first, second)
}
