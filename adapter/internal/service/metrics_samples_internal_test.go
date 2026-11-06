package service

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

// A code from an agent is stored only when the control plane knows it. Any
// other value becomes one fixed code, so nothing an agent sends can reach the
// column as text that is too long or not valid UTF-8.
func TestMonitoringCodeKeepsOnlyKnownCodes(t *testing.T) {
	cases := map[string]string{
		"":                             "",
		"  ":                           "",
		MonitoringNotConfigured:        MonitoringNotConfigured,
		MonitoringUnreachable:          MonitoringUnreachable,
		MonitoringUnavailable:          MonitoringUnavailable,
		" " + MonitoringNoTarget + " ": MonitoringNoTarget,
		"something-new":                monitoringUnknown,
		strings.Repeat("a", 63) + "é":  monitoringUnknown,
		strings.Repeat("x", 500):       monitoringUnknown,
	}
	for in, want := range cases {
		got := monitoringCode(in)
		assert.Equal(t, want, got, "%q", in)
		assert.True(t, utf8.ValidString(got))
		assert.LessOrEqual(t, len(got), 64)
	}
	assert.Equal(t, noMonitoringReadings, monitoringPendingReason(monitoringUnknown))
}
