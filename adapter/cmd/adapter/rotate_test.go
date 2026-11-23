package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/everest"
	"github.com/percona/pds/adapter/internal/client/kube"
	"github.com/percona/pds/adapter/internal/engine"
)

type fakeInstances struct {
	instance *everest.Instance
	err      error
	asked    string
}

func (f *fakeInstances) GetInstance(_ context.Context, namespace, name string) (*everest.Instance, error) {
	f.asked = namespace + "/" + name

	return f.instance, f.err
}

type fakePatcher struct {
	patched string
	values  map[string]string
	err     error
}

func (f *fakePatcher) PatchSecret(_ context.Context, namespace, name string, values map[string]string) error {
	f.patched = namespace + "/" + name
	f.values = values

	return f.err
}

func restored(phase, dataSource string) *everest.Instance {
	status := everest.InstanceStatus{Phase: phase}
	if dataSource != "" {
		status.Conditions = []everest.Condition{{Type: everest.ConditionDataSourceReady, Status: dataSource}}
	}

	return &everest.Instance{Status: status}
}

func TestRotationWritesNewPasswordsIntoTheCopysUsersSecret(t *testing.T) {
	instances := &fakeInstances{instance: restored(everest.PhaseReady, "True")}
	secrets := &fakePatcher{}

	err := newRotateUsers(instances, secrets)(context.Background(), "pds", "orders-copy")

	require.NoError(t, err)
	assert.Equal(t, "pds/orders-copy", instances.asked)
	assert.Equal(t, "pds/everest-secrets-orders-copy", secrets.patched)
	assert.Len(t, secrets.values, 5)
	assert.Regexp(t, `^[A-Za-z0-9]{24}$`, secrets.values["MONGODB_DATABASE_ADMIN_PASSWORD"])
}

// Until the restore has finished the copy's Secret is the source's, and the
// provider may still copy it again, so nothing is written.
func TestRotationWaitsForTheRestoreToFinish(t *testing.T) {
	cases := map[string]*everest.Instance{
		"restoring":           restored(everest.PhaseRestoring, "False"),
		"ready before seeded": restored(everest.PhaseReady, ""),
		"data source pending": restored(everest.PhaseReady, "False"),
		"failed":              restored(everest.PhaseFailed, "False"),
	}
	for name, instance := range cases {
		t.Run(name, func(t *testing.T) {
			secrets := &fakePatcher{}

			err := newRotateUsers(&fakeInstances{instance: instance}, secrets)(context.Background(), "pds", "orders-copy")

			require.ErrorIs(t, err, engine.ErrRestoreInProgress)
			assert.Empty(t, secrets.patched)
		})
	}
}

func TestRotationWaitsForTheCopyToExist(t *testing.T) {
	secrets := &fakePatcher{}

	err := newRotateUsers(&fakeInstances{err: everest.ErrNotFound}, secrets)(context.Background(), "pds", "orders-copy")

	require.ErrorIs(t, err, engine.ErrRestoreInProgress)
	assert.Empty(t, secrets.patched)
}

func TestRotationReportsWhatItCouldNotDo(t *testing.T) {
	ready := &fakeInstances{instance: restored(everest.PhaseReady, "True")}

	forbidden := newRotateUsers(ready, &fakePatcher{err: kube.ErrForbidden})(context.Background(), "pds", "orders-copy")
	require.ErrorIs(t, forbidden, kube.ErrForbidden)
	assert.NotErrorIs(t, forbidden, engine.ErrRestoreInProgress)

	missing := newRotateUsers(ready, &fakePatcher{err: kube.ErrSecretNotFound})(context.Background(), "pds", "orders-copy")
	require.ErrorIs(t, missing, kube.ErrSecretNotFound)
	assert.NotErrorIs(t, missing, engine.ErrRestoreInProgress)

	unreadable := newRotateUsers(&fakeInstances{err: errors.New("down")}, &fakePatcher{})(context.Background(), "pds", "orders-copy")
	require.Error(t, unreadable)
	assert.NotErrorIs(t, unreadable, engine.ErrRestoreInProgress)
}
