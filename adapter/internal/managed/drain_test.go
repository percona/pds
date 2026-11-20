package managed_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/managed"
)

type fakeCleaner struct {
	suspended []string
	deleted   []string
	lbLeft    int
	failOn    string
	userDone  bool
	asked     int
}

func (f *fakeCleaner) EnsureConfigMap(_ context.Context, namespace, name string, data map[string]string) (map[string]string, error) {
	f.asked++
	if f.failOn == "request" {
		return nil, errors.New("the Kubernetes API answered Forbidden")
	}
	held := map[string]string{}
	for k, v := range data {
		held[k] = v
	}
	if f.userDone {
		held["removed"] = "true"
	}
	f.deleted = append(f.deleted, "ConfigMap/"+namespace+"/"+name)

	return held, nil
}

func (f *fakeCleaner) Suspend(_ context.Context, _, kind, namespace, name string) error {
	f.suspended = append(f.suspended, kind+"/"+namespace+"/"+name)
	if f.failOn == "suspend" {
		return errors.New("the Kubernetes API answered Forbidden")
	}

	return nil
}

func (f *fakeCleaner) DeleteAll(_ context.Context, _, kind, namespace string) (int, error) {
	f.deleted = append(f.deleted, kind+"/"+namespace)

	return 0, nil
}

func (f *fakeCleaner) DeleteLoadBalancerServices(context.Context) (int, error) {
	f.deleted = append(f.deleted, "LoadBalancer Services")

	return f.lbLeft, nil
}

func drainWith(c *fakeCleaner, dialErr error) *managed.Drain {
	return managed.NewDrain(func(managed.KubeAccess) (managed.Cleaner, error) { return c, dialErr })
}

func TestRemoveDatabasesStopsFluxBeforeItDeletesTheInstances(t *testing.T) {
	c := &fakeCleaner{}

	left, err := drainWith(c, nil).RemoveDatabases(t.Context(), managed.KubeAccess{})
	require.NoError(t, err)

	assert.Zero(t, left)
	assert.Equal(t, []string{
		"Kustomization/flux-system/instances", "Kustomization/flux-system/platform", "Kustomization/flux-system/pds",
	}, c.suspended, "Git would otherwise put the databases back")
	assert.Equal(t, []string{"Instance/"}, c.deleted, "every Instance in every namespace")
}

func TestRemoveDatabasesStopsWhenFluxCannotBeSuspended(t *testing.T) {
	c := &fakeCleaner{failOn: "suspend"}

	_, err := drainWith(c, nil).RemoveDatabases(t.Context(), managed.KubeAccess{})

	require.Error(t, err)
	assert.Empty(t, c.deleted, "nothing is deleted while Flux could still recreate it")
}

func TestRemoveVolumeUsersDeletesLoadBalancerServicesAndVolumeClaims(t *testing.T) {
	c := &fakeCleaner{lbLeft: 1}

	left, err := drainWith(c, nil).RemoveVolumeUsers(t.Context(), managed.KubeAccess{})
	require.NoError(t, err)

	assert.Equal(t, 1, left)
	assert.Equal(t, []string{"LoadBalancer Services", "PersistentVolumeClaim/"}, c.deleted)
}

func TestDrainReportsAnEnvironmentItCannotReach(t *testing.T) {
	_, err := drainWith(&fakeCleaner{}, errors.New("dial: i/o timeout")).RemoveDatabases(t.Context(), managed.KubeAccess{})

	require.Error(t, err)
}

func TestRemoveBackupUserAsksTheAgentAndReportsOneLeftUntilItIsDone(t *testing.T) {
	c := &fakeCleaner{}
	drain := drainWith(c, nil)

	left, err := drain.RemoveBackupUser(t.Context(), managed.KubeAccess{})
	require.NoError(t, err)
	assert.Equal(t, 1, left)
	assert.Equal(t, []string{"ConfigMap/everest-system/pds-backup-user"}, c.deleted, "the request is made in the release's namespace")

	c.userDone = true
	left, err = drain.RemoveBackupUser(t.Context(), managed.KubeAccess{})
	require.NoError(t, err)
	assert.Zero(t, left)
	assert.Equal(t, 2, c.asked)
}

func TestRemoveBackupUserReturnsWhatTheEnvironmentRefused(t *testing.T) {
	_, err := drainWith(&fakeCleaner{failOn: "request"}, nil).RemoveBackupUser(t.Context(), managed.KubeAccess{})

	require.Error(t, err)
}
