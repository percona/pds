package engine_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/engine"
)

func TestNormalizeSourceRanges(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{name: "nil stays nil", in: nil, want: nil},
		{name: "empty stays nil", in: []string{}, want: nil},
		{name: "blank entries are dropped", in: []string{" ", ""}, want: nil},
		{name: "cidr kept", in: []string{"10.0.0.0/8"}, want: []string{"10.0.0.0/8"}},
		{name: "host bits cleared", in: []string{"10.1.2.3/8"}, want: []string{"10.0.0.0/8"}},
		{name: "bare ipv4 becomes a /32", in: []string{"203.0.113.7"}, want: []string{"203.0.113.7/32"}},
		{name: "bare ipv6 becomes a /128", in: []string{"2001:db8::1"}, want: []string{"2001:db8::1/128"}},
		{name: "whitespace trimmed", in: []string{"  192.168.0.0/16 "}, want: []string{"192.168.0.0/16"}},
		{
			name: "duplicates collapse in first seen order",
			in:   []string{"10.0.0.0/8", "203.0.113.7", "10.0.0.0/8", "203.0.113.7/32"},
			want: []string{"10.0.0.0/8", "203.0.113.7/32"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := engine.NormalizeSourceRanges(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNormalizeSourceRangesRefusesWhatIsNotAnAddress(t *testing.T) {
	for _, bad := range []string{"10.0.0.0/33", "example.com", "10.0.0", "10.0.0.0/8,192.168.0.0/16"} {
		t.Run(bad, func(t *testing.T) {
			_, err := engine.NormalizeSourceRanges([]string{"10.0.0.0/8", bad})

			var rangeErr *engine.SourceRangeError
			require.ErrorAs(t, err, &rangeErr)
			assert.True(t, errors.Is(err, engine.ErrInvalidSourceRange))
			assert.Equal(t, bad, rangeErr.Value)
			assert.Contains(t, rangeErr.UserMessage(), bad)
		})
	}
}

func TestNormalizeSourceRangesCapsTheList(t *testing.T) {
	in := make([]string, engine.MaxSourceRanges+1)
	for i := range in {
		in[i] = fmt.Sprintf("10.0.%d.0/24", i)
	}

	_, err := engine.NormalizeSourceRanges(in)

	var rangeErr *engine.SourceRangeError
	require.ErrorAs(t, err, &rangeErr)
	assert.True(t, rangeErr.TooMany)
	assert.Contains(t, rangeErr.UserMessage(), "32")
}

func TestCheckAccess(t *testing.T) {
	tests := []struct {
		name string
		in   engine.CreateInput
		want engine.Exposure
	}{
		{
			name: "a database defaults to a private load balancer",
			in:   engine.CreateInput{},
			want: engine.Exposure{External: true, Private: true},
		},
		{
			name: "private asked for",
			in:   engine.CreateInput{Access: engine.AccessPrivate},
			want: engine.Exposure{External: true, Private: true},
		},
		{
			name: "public with an allow list",
			in:   engine.CreateInput{Access: engine.AccessPublic, SourceRanges: []string{"203.0.113.7"}},
			want: engine.Exposure{External: true, SourceRanges: []string{"203.0.113.7/32"}},
		},
		{
			name: "public open to every address",
			in:   engine.CreateInput{Access: engine.AccessPublic, SourceRanges: []string{"0.0.0.0/0"}},
			want: engine.Exposure{External: true, SourceRanges: []string{"0.0.0.0/0"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := engine.CheckAccess(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCheckAccessRefusals(t *testing.T) {
	t.Run("an unknown access value", func(t *testing.T) {
		_, err := engine.CheckAccess(engine.CreateInput{Access: "internal"})

		var accessErr *engine.AccessError
		require.ErrorAs(t, err, &accessErr)
		assert.True(t, errors.Is(err, engine.ErrInvalidAccess))
		assert.Equal(t, "Access must be private or public.", accessErr.UserMessage())
	})

	t.Run("an allow list on a private database", func(t *testing.T) {
		_, err := engine.CheckAccess(engine.CreateInput{
			Access:       engine.AccessPrivate,
			SourceRanges: []string{"10.0.0.0/8"},
		})

		var rangeErr *engine.SourceRangeError
		require.ErrorAs(t, err, &rangeErr)
		assert.True(t, rangeErr.Unexposed)
		assert.Equal(t, "Allowed addresses apply to a public database only.", rangeErr.UserMessage())
	})

	t.Run("an allow list with the default access", func(t *testing.T) {
		_, err := engine.CheckAccess(engine.CreateInput{SourceRanges: []string{"10.0.0.0/8"}})

		require.ErrorIs(t, err, engine.ErrInvalidSourceRange)
	})

	t.Run("a bad range is reported before the access mismatch", func(t *testing.T) {
		_, err := engine.CheckAccess(engine.CreateInput{
			Access:       engine.AccessPublic,
			SourceRanges: []string{"not-an-ip"},
		})

		var rangeErr *engine.SourceRangeError
		require.ErrorAs(t, err, &rangeErr)
		assert.Equal(t, "not-an-ip", rangeErr.Value)
	})
}

func TestExposureVisibility(t *testing.T) {
	assert.Equal(t, engine.VisibilityCluster, engine.Exposure{}.Visibility())
	assert.Equal(t, engine.VisibilityPrivate, engine.Exposure{External: true, Private: true}.Visibility())
	assert.Equal(t, engine.VisibilityPublic, engine.Exposure{External: true}.Visibility())
}

func TestPrivateLoadBalancerIsReachableOnceItHasAnAddress(t *testing.T) {
	assert.True(t, engine.Exposure{External: true, Private: true, Address: "10.0.4.12"}.Reachable())
	assert.False(t, engine.Exposure{External: true, Private: true}.Reachable())
}

func TestPubliclyReachable(t *testing.T) {
	assert.True(t, engine.Exposure{External: true, Address: "198.51.100.20"}.PubliclyReachable())
	assert.False(t, engine.Exposure{External: true}.PubliclyReachable(), "no address yet")
	assert.False(t, engine.Exposure{External: true, Private: true, Address: "10.0.4.12"}.PubliclyReachable())
	assert.False(t, engine.Exposure{Address: "10.0.4.12"}.PubliclyReachable())
}
