package managed_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/managed"
	"github.com/percona/pds/adapter/internal/store"
)

type memoryRecords struct {
	rows    map[string]store.ManagedEnvironment
	removed []string
}

func (m *memoryRecords) Create(_ context.Context, in store.NewManagedEnvironment, _ time.Time) (store.ManagedEnvironment, error) {
	if m.rows == nil {
		m.rows = map[string]store.ManagedEnvironment{}
	}
	if _, dup := m.rows[in.Name]; dup {
		return store.ManagedEnvironment{}, store.ErrManagedEnvironmentExists
	}
	row := store.ManagedEnvironment{ID: in.ID, TeamID: in.TeamID, Name: in.Name, ConnectionID: in.ConnectionID, Region: in.Region}
	m.rows[in.Name] = row

	return row, nil
}

func (m *memoryRecords) Remove(_ context.Context, _ uint, name string) error {
	m.removed = append(m.removed, name)
	delete(m.rows, name)

	return nil
}

type fakeVerifier struct {
	conn store.CloudConnection
	err  error
	ids  []string
}

func (f *fakeVerifier) Verify(_ context.Context, _ auth.Scope, id string) (store.CloudConnection, error) {
	f.ids = append(f.ids, id)

	return f.conn, f.err
}

type fakeRuns struct {
	started []string
	err     error
}

func (f *fakeRuns) Start(_ context.Context, team uint, environment, kind string) (store.ProvisioningRun, error) {
	f.started = append(f.started, kind+"/"+environment)

	return store.ProvisioningRun{ID: 7, TeamID: team, Environment: environment, Kind: kind, Status: store.RunQueued}, f.err
}

func newCreator(conn store.CloudConnection) (*managed.Creator, *memoryRecords, *fakeVerifier, *fakeRuns) {
	records, verifier, runs := &memoryRecords{}, &fakeVerifier{conn: conn}, &fakeRuns{}

	return managed.NewCreator(records, verifier, runs, managed.CreatorSettings{
		OutgoingCIDRs: []string{"203.0.113.7/32"}, ChartVersion: "0.1.0", ImageRepository: "ghcr.io/percona/pds-adapter",
	}), records, verifier, runs
}

var readyConnection = store.CloudConnection{ID: "aaaaaaaaaaaa", AccountID: "111122223333", State: store.CloudReady, AllowedRegions: []string{"eu-west-1"}}

var scope = auth.Scope{UserID: 3, Team: auth.Team{ID: 9}}

func TestCreateChecksTheConnectionThenRecordsAndStartsTheRun(t *testing.T) {
	c, records, verifier, runs := newCreator(readyConnection)

	got, err := c.Create(t.Context(), scope, managed.CreateInput{ConnectionID: "aaaaaaaaaaaa", Name: "orders", Region: "eu-west-1"})
	require.NoError(t, err)

	assert.Equal(t, []string{"aaaaaaaaaaaa"}, verifier.ids, "the check runs before every create")
	assert.Regexp(t, `^[0-9a-f]{12}$`, got.Environment.ID)
	assert.Equal(t, "eu-west-1", records.rows["orders"].Region)
	assert.Equal(t, []string{"create/orders"}, runs.started)
	assert.Equal(t, uint(7), got.Run.ID)
}

func TestCreateRefusesBeforeAnythingIsBuilt(t *testing.T) {
	notReady := readyConnection
	notReady.State = store.CloudUnreachable
	input := managed.CreateInput{ConnectionID: "aaaaaaaaaaaa", Name: "orders", Region: "eu-west-1"}

	tests := []struct {
		name   string
		mutate func(*managed.CreatorSettings, *managed.CreateInput)
		conn   store.CloudConnection
		want   error
	}{
		{"no declared outgoing addresses", func(s *managed.CreatorSettings, _ *managed.CreateInput) { s.OutgoingCIDRs = nil }, readyConnection, managed.ErrUnavailable},
		{"no published chart", func(s *managed.CreatorSettings, _ *managed.CreateInput) { s.ChartVersion = "" }, readyConnection, managed.ErrUnavailable},
		{"a name that is not an environment name", func(_ *managed.CreatorSettings, in *managed.CreateInput) { in.Name = "Not Valid" }, readyConnection, managed.ErrInvalid},
		{"a region the connection does not allow", func(_ *managed.CreatorSettings, in *managed.CreateInput) { in.Region = "us-east-1" }, readyConnection, managed.ErrInvalid},
		{"a connection that is not ready", nil, notReady, managed.ErrConnectionNotReady},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, records, _, runs := newCreator(tt.conn)
			settings := managed.CreatorSettings{OutgoingCIDRs: []string{"203.0.113.7/32"}, ChartVersion: "0.1.0", ImageRepository: "img"}
			in := input
			if tt.mutate != nil {
				tt.mutate(&settings, &in)
				c = managed.NewCreator(records, &fakeVerifier{conn: tt.conn}, runs, settings)
			}

			_, err := c.Create(t.Context(), scope, in)
			require.ErrorIs(t, err, tt.want)
			assert.Empty(t, records.rows)
			assert.Empty(t, runs.started)
		})
	}
}

func TestCreateKeepsAnotherTeamsConnectionNotFound(t *testing.T) {
	c, _, verifier, _ := newCreator(readyConnection)
	verifier.err = store.ErrCloudConnectionUnknown

	_, err := c.Create(t.Context(), scope, managed.CreateInput{ConnectionID: "bbbbbbbbbbbb", Name: "orders", Region: "eu-west-1"})
	require.ErrorIs(t, err, store.ErrCloudConnectionUnknown)
}

func TestCreateRefusesADuplicateNameInTheTeam(t *testing.T) {
	c, _, _, _ := newCreator(readyConnection)
	in := managed.CreateInput{ConnectionID: "aaaaaaaaaaaa", Name: "orders", Region: "eu-west-1"}
	_, err := c.Create(t.Context(), scope, in)
	require.NoError(t, err)

	_, err = c.Create(t.Context(), scope, in)
	require.ErrorIs(t, err, store.ErrManagedEnvironmentExists)
}

func TestCreateForgetsTheRecordWhenTheRunCannotStart(t *testing.T) {
	c, records, _, runs := newCreator(readyConnection)
	runs.err = errors.New("database down")

	_, err := c.Create(t.Context(), scope, managed.CreateInput{ConnectionID: "aaaaaaaaaaaa", Name: "orders", Region: "eu-west-1"})
	require.Error(t, err)
	assert.Empty(t, records.rows, "a name is not held by an environment that was never started")
}
