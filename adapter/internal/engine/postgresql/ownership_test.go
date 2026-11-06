package postgresql_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/everest"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/engine/oe"
	"github.com/percona/pds/adapter/internal/engine/postgresql"
)

func unlabeledPG(name string) *everest.Instance {
	in := pgInstance(name, 1, "1", "2Gi", "20Gi")
	in.Metadata.Labels = nil

	return in
}

func TestListOmitsInstancesWithoutTheManagedByLabel(t *testing.T) {
	p, fake := newPlugin(t)
	seedProfile(t, fake, "orders", "dev")

	_, err := fake.CreateInstance(context.Background(), postgresql.DefaultNamespace, unlabeledPG("hand-rolled"))
	require.NoError(t, err)

	got, err := p.List(context.Background())
	require.NoError(t, err)

	require.Len(t, got, 1)
	assert.Equal(t, "orders", got[0].Name)
}

func TestListKeepsAManagedInstance(t *testing.T) {
	p, fake := newPlugin(t)
	created := seedProfile(t, fake, "orders", "dev")

	got, err := p.List(context.Background())
	require.NoError(t, err)

	require.Len(t, got, 1)
	assert.Equal(t, created, got[0].ID)
}

func TestIDOperationsRejectInstancesOutsideTheBoundary(t *testing.T) {
	ctx := context.Background()
	scale := engine.ScaleInput{CPUCores: intPtr(2)}

	ops := []struct {
		name      string
		call      func(*postgresql.Plugin, string) error
		mutation  string
		needsStor bool
	}{
		{
			name: "scale",
			call: func(p *postgresql.Plugin, id string) error {
				return p.Scale(ctx, id, scale)
			},
			mutation: "UpdateInstance",
		},
		{
			name: "backup",
			call: func(p *postgresql.Plugin, id string) error {
				_, err := p.Backup(ctx, id)

				return err
			},
			mutation:  "CreateBackup",
			needsStor: true,
		},
		{
			name: "backups",
			call: func(p *postgresql.Plugin, id string) error {
				_, err := p.Backups(ctx, id)

				return err
			},
		},
		{
			name: "delete",
			call: func(p *postgresql.Plugin, id string) error {
				return p.Delete(ctx, id)
			},
			mutation: "DeleteInstance",
		},
	}

	cases := []struct {
		name     string
		id       string
		seed     func(*testing.T, *fakeEverest)
		allowGet bool
	}{
		{
			name: "unlabeled instance in the managed namespace",
			id:   "pds.hand-rolled",
			seed: func(t *testing.T, fake *fakeEverest) {
				t.Helper()
				_, err := fake.CreateInstance(ctx, postgresql.DefaultNamespace, unlabeledPG("hand-rolled"))
				require.NoError(t, err)
			},
			allowGet: true,
		},
		{
			name: "instance in another namespace",
			id:   "kube-system.orders",
			seed: func(t *testing.T, fake *fakeEverest) {
				t.Helper()
				_, err := fake.CreateInstance(ctx, "kube-system", pgInstance("orders", 1, "1", "2Gi", "20Gi"))
				require.NoError(t, err)
			},
		},
		{
			name: "wrong provider",
			id:   "pds.reporting",
			seed: func(t *testing.T, fake *fakeEverest) {
				t.Helper()
				in := pgInstance("reporting", 1, "1", "2Gi", "20Gi")
				in.Spec.ProviderRef.Name = everest.ProviderPXC
				_, err := fake.CreateInstance(ctx, postgresql.DefaultNamespace, in)
				require.NoError(t, err)
			},
			allowGet: true,
		},
	}

	for _, op := range ops {
		for _, tc := range cases {
			t.Run(op.name+"/"+tc.name, func(t *testing.T) {
				var (
					p    *postgresql.Plugin
					fake *fakeEverest
				)
				if op.needsStor {
					p, fake = newPluginWithBackups(t)
				} else {
					p, fake = newPlugin(t)
				}
				tc.seed(t, fake)
				fake.resetCalls()

				err := op.call(p, tc.id)

				require.Error(t, err)
				assert.ErrorIs(t, err, oe.ErrInstanceNotFound)
				assert.False(t, errors.Is(err, oe.ErrNamespaceNotManaged),
					"an id outside the boundary uses the same not-found as a missing instance")

				seen := fake.seen()
				if !tc.allowGet {
					for _, call := range seen {
						assert.False(t, strings.HasPrefix(call, "GetInstance "),
							"another namespace must not be read from Everest: %s", call)
					}
				}
				if op.mutation != "" {
					for _, call := range seen {
						assert.False(t, strings.HasPrefix(call, op.mutation),
							"mutation %s must not run: %s", op.mutation, call)
					}
				}
				if strings.HasPrefix(tc.id, "kube-system.") {
					assert.NotNil(t, fake.stored("kube-system", "orders"),
						"the external instance stays where it is")
				}
				if strings.Contains(tc.name, "unlabeled") {
					assert.NotNil(t, fake.stored(postgresql.DefaultNamespace, "hand-rolled"),
						"the unlabeled instance stays where it is")
				}
			})
		}
	}
}

func TestIDOperationsAcceptAManagedInstance(t *testing.T) {
	ctx := context.Background()
	p, fake := newPluginWithBackups(t)
	created := seedProfile(t, fake, "orders", "dev")

	require.NoError(t, p.Scale(ctx, created, engine.ScaleInput{CPUCores: intPtr(2)}))
	assert.Equal(t, "2", fake.stored(postgresql.DefaultNamespace, "orders").EngineComponent().CPU())

	backup, err := p.Backup(ctx, created)
	require.NoError(t, err)
	assert.NotEmpty(t, backup.ID)

	require.NoError(t, p.Delete(ctx, created))
	assert.Nil(t, fake.stored(postgresql.DefaultNamespace, "orders"))
}

// legacyPG is an instance as a release before the rename to PDS wrote it: the
// old managed-by value and the old label domain.
func legacyPG(name, profile string) *everest.Instance {
	in := pgInstance(name, 1, "1", "2Gi", "20Gi")
	in.Metadata.Labels = map[string]string{
		oe.LabelManagedBy:     oe.LegacyManagedBy,
		oe.LegacyLabelProfile: profile,
	}

	return in
}

func TestListKeepsAnInstanceLabeledBeforeTheRename(t *testing.T) {
	p, fake := newPlugin(t)
	_, err := fake.CreateInstance(context.Background(), postgresql.DefaultNamespace, legacyPG("orders", "dev"))
	require.NoError(t, err)

	got, err := p.List(context.Background())
	require.NoError(t, err)

	require.Len(t, got, 1)
	assert.Equal(t, "orders", got[0].Name)
	assert.Equal(t, "dev", got[0].Profile)
}
