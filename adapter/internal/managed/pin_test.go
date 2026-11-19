package managed_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/managed"
)

func TestPinCopyMatchesTheChart(t *testing.T) {
	chart, err := os.ReadFile("../../../charts/pds/compatibility.yaml")
	require.NoError(t, err)

	assert.Equal(t, string(chart), managed.Pin, "copy charts/pds/compatibility.yaml here when it changes")
}

func TestThePinnedUpgradePathsParse(t *testing.T) {
	_, err := managed.PinnedUpgrades()

	require.NoError(t, err)
}

func TestUpgradePathsReadTheMapOfTheSupportBlock(t *testing.T) {
	paths, err := managed.ParseUpgrades([]byte(`
support:
  managedUpgrade:
    from:
      0.1.0: [0.2.0, 0.1.5]
      0.2.0: [0.3.0]
`))

	require.NoError(t, err)
	next, ok := paths.Next("0.1.0", "")
	assert.True(t, ok)
	assert.Equal(t, "0.2.0", next, "the newest version the pin lists")
}

func TestAPinWithoutAManagedUpgradeBlockHasNoPaths(t *testing.T) {
	paths, err := managed.ParseUpgrades([]byte("support: {}\n"))

	require.NoError(t, err)
	_, ok := paths.Next("0.1.0", "")
	assert.False(t, ok)
}

func TestAPinThatIsNotYAMLIsAnError(t *testing.T) {
	_, err := managed.ParseUpgrades([]byte("support: ["))

	require.Error(t, err)
}

func TestAPinWithAVersionThatIsNotAReleaseIsRefused(t *testing.T) {
	_, err := managed.ParseUpgrades([]byte("support:\n  managedUpgrade:\n    from:\n      0.1.0: [latest]\n"))

	require.Error(t, err)
}

func TestNextNeverMovesAnEnvironmentToAVersionThePinDoesNotListForIt(t *testing.T) {
	paths := managed.UpgradePaths{"0.1.0": {"0.2.0"}}

	_, ok := paths.Next("0.1.5", "")
	assert.False(t, ok, "0.1.5 has no entry")
	_, ok = paths.Next("0.2.0", "")
	assert.False(t, ok, "0.2.0 is the end of the path")
}

func TestNextSkipsAVersionThatWasRolledBackUntilANewerOneArrives(t *testing.T) {
	paths := managed.UpgradePaths{"0.1.0": {"0.2.0", "0.3.0"}}

	next, ok := paths.Next("0.1.0", "0.2.0")
	assert.True(t, ok)
	assert.Equal(t, "0.3.0", next, "a newer version than the skipped one")

	_, ok = paths.Next("0.1.0", "0.3.0")
	assert.False(t, ok, "nothing is newer than the version that failed")
}

func TestVersionsCompareByNumberNotByText(t *testing.T) {
	paths := managed.UpgradePaths{"0.9.0": {"0.10.0", "0.9.5"}}

	next, ok := paths.Next("0.9.0", "")

	assert.True(t, ok)
	assert.Equal(t, "0.10.0", next)
}

func TestAReleaseCandidateIsOlderThanItsRelease(t *testing.T) {
	paths := managed.UpgradePaths{"0.1.0": {"0.2.0-rc.1", "0.2.0"}}

	next, ok := paths.Next("0.1.0", "")

	assert.True(t, ok)
	assert.Equal(t, "0.2.0", next)
}
