package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	clientfake "k8s.io/client-go/kubernetes/fake"

	"github.com/percona/pds/adapter/internal/agent"
	"github.com/percona/pds/adapter/internal/client/backupkey"
	"github.com/percona/pds/adapter/internal/client/kube"
	"github.com/percona/pds/adapter/internal/config"
	"github.com/percona/pds/adapter/internal/managed"
)

func newBackupEnvironment() (kubeBackupEnvironment, *kube.Client) {
	client := kube.New(clientfake.NewClientset())

	return kubeBackupEnvironment{client: client, namespace: "everest-system", secret: "pds-s3"}, client
}

func TestTheBackupSecretIsWrittenAndReadBackAndAbsentIsNothing(t *testing.T) {
	environment, _ := newBackupEnvironment()

	got, err := environment.Secret(t.Context())
	require.NoError(t, err)
	assert.Empty(t, got)

	require.NoError(t, environment.WriteSecret(t.Context(), map[string]string{"AWS_ACCESS_KEY_ID": "AKIA", "AWS_SECRET_ACCESS_KEY": "shh"}))

	got, err = environment.Secret(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "AKIA", got["AWS_ACCESS_KEY_ID"])
}

func TestARemovalNobodyAskedForIsNotRequested(t *testing.T) {
	environment, _ := newBackupEnvironment()

	requested, done, err := environment.Removal(t.Context())

	require.NoError(t, err)
	assert.False(t, requested)
	assert.False(t, done)
}

func TestARemovalTheControlPlaneAskedForIsRequestedUntilTheAgentMarksItDone(t *testing.T) {
	environment, client := newBackupEnvironment()
	require.NoError(t, client.PutConfigMap(t.Context(), "everest-system", managed.BackupUserRequest, map[string]string{"request": "remove"}))

	requested, done, err := environment.Removal(t.Context())
	require.NoError(t, err)
	assert.True(t, requested)
	assert.False(t, done)

	require.NoError(t, environment.MarkRemoved(t.Context()))

	requested, done, err = environment.Removal(t.Context())
	require.NoError(t, err)
	assert.True(t, requested, "the request stays, so a restarted agent never makes the key again")
	assert.True(t, done)
}

func TestTheBackupSpecIsWhatTheReleaseAskedFor(t *testing.T) {
	got := backupSpec(config.AgentBackup{
		User: "pds-k3j9x2m4p1q7-backup", Path: "/pds/aaaaaaaaaaaa/", BoundaryARN: "arn:aws:iam::1:policy/b",
		Bucket: "pds-123456789012-k3j9x2m4p1q7", Tag: "k3j9x2m4p1q7", Secret: "pds-s3", Region: "eu-west-1",
	})

	assert.Equal(t, backupkey.Spec{
		Name: "pds-k3j9x2m4p1q7-backup", Path: "/pds/aaaaaaaaaaaa/", BoundaryARN: "arn:aws:iam::1:policy/b",
		Bucket: "pds-123456789012-k3j9x2m4p1q7", EnvironmentTag: "k3j9x2m4p1q7",
	}, got)
}

// The release's backup storage job waits for the Secret the agent writes the
// key to, so an agent the release asks for a key must keep one.
func TestAnAgentTheReleaseAsksForABackupKeyKeepsIt(t *testing.T) {
	t.Setenv("AWS_PROFILE", "")
	cfg := config.Config{AgentBackup: config.AgentBackup{
		User: "pds-k3j9x2m4p1q7-backup", Path: "/pds/aaaaaaaaaaaa/", BoundaryARN: "arn:aws:iam::1:policy/b",
		Bucket: "pds-123456789012-k3j9x2m4p1q7", Tag: "k3j9x2m4p1q7", Secret: "pds-s3", Region: "eu-west-1",
	}}

	opts, err := environmentOptions(t.Context(), cfg, kube.New(clientfake.NewClientset()), testLogger(), agent.Options{})

	require.NoError(t, err)
	assert.NotNil(t, opts.BackupKey)
}

func TestAnEnvironmentThatWasNotAskedToKeepAKeyKeepsNone(t *testing.T) {
	reconcile, err := newBackupKey(t.Context(), config.Config{}, kube.New(clientfake.NewClientset()))

	require.NoError(t, err)
	assert.Nil(t, reconcile)
}
