package agent_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/agent"
	"github.com/percona/pds/adapter/internal/client/backupkey"
)

var keySpec = backupkey.Spec{Name: "pds-k3j9x2m4p1q7-backup", Bucket: "pds-123456789012-k3j9x2m4p1q7", EnvironmentTag: "k3j9x2m4p1q7"}

// iamAccount is the agent's IAM user, as the agent sees it.
type iamAccount struct {
	ensured, minted, removed int
	user                     bool
	err                      error
}

func (a *iamAccount) Ensure(context.Context, backupkey.Spec) error {
	if a.err != nil {
		return a.err
	}
	a.ensured++
	a.user = true

	return nil
}

func (a *iamAccount) NewKey(context.Context, backupkey.Spec) (backupkey.Key, error) {
	if a.err != nil {
		return backupkey.Key{}, a.err
	}
	a.minted++

	return backupkey.Key{ID: "AKIAFAKE", Secret: "shh"}, nil
}

func (a *iamAccount) Remove(context.Context, backupkey.Spec) error {
	if a.err != nil {
		return a.err
	}
	a.removed++
	a.user = false

	return nil
}

// environment is the Secret and the removal request the agent keeps in its own
// namespace.
type environment struct {
	secret             map[string]string
	requested, removed bool
	secretWrites       int
	failWrite          error
}

func (e *environment) Secret(context.Context) (map[string]string, error) { return e.secret, nil }

func (e *environment) WriteSecret(_ context.Context, values map[string]string) error {
	if e.failWrite != nil {
		return e.failWrite
	}
	e.secretWrites++
	e.secret = values

	return nil
}

func (e *environment) Removal(context.Context) (requested, done bool, err error) {
	return e.requested, e.removed, nil
}

func (e *environment) MarkRemoved(context.Context) error {
	e.removed = true

	return nil
}

func TestTheAgentMakesTheUserAndWritesTheKeyStraightToTheSecret(t *testing.T) {
	account, env := &iamAccount{}, &environment{}

	require.NoError(t, agent.BackupKey{Users: account, Cluster: env, Spec: keySpec}.Reconcile(t.Context()))

	assert.Equal(t, map[string]string{"AWS_ACCESS_KEY_ID": "AKIAFAKE", "AWS_SECRET_ACCESS_KEY": "shh"}, env.secret)
	assert.Equal(t, 1, account.ensured)
	assert.Equal(t, 1, account.minted)
}

func TestTheAgentLeavesAKeyThatIsAlreadyInTheSecret(t *testing.T) {
	account := &iamAccount{}
	env := &environment{secret: map[string]string{"AWS_ACCESS_KEY_ID": "AKIAOLD", "AWS_SECRET_ACCESS_KEY": "old"}}
	key := agent.BackupKey{Users: account, Cluster: env, Spec: keySpec}

	require.NoError(t, key.Reconcile(t.Context()))
	require.NoError(t, key.Reconcile(t.Context()))

	assert.Equal(t, "AKIAOLD", env.secret["AWS_ACCESS_KEY_ID"], "a key cannot be read back from IAM, so a working one is never replaced")
	assert.Zero(t, account.minted)
	assert.Zero(t, account.ensured)
}

func TestTheAgentMakesANewKeyWhenTheSecretLostIt(t *testing.T) {
	account := &iamAccount{}
	env := &environment{secret: map[string]string{"AWS_ACCESS_KEY_ID": "AKIAOLD"}}

	require.NoError(t, agent.BackupKey{Users: account, Cluster: env, Spec: keySpec}.Reconcile(t.Context()))

	assert.Equal(t, "shh", env.secret["AWS_SECRET_ACCESS_KEY"])
	assert.Equal(t, 1, account.minted)
}

func TestAKeyThatCouldNotBeWrittenIsMadeAgainOnTheNextBeat(t *testing.T) {
	account := &iamAccount{}
	env := &environment{failWrite: errors.New("the API is down")}
	key := agent.BackupKey{Users: account, Cluster: env, Spec: keySpec}

	require.Error(t, key.Reconcile(t.Context()))
	env.failWrite = nil
	require.NoError(t, key.Reconcile(t.Context()))

	assert.Equal(t, 2, account.minted, "the first key's secret was lost with the failed write")
	assert.Equal(t, "shh", env.secret["AWS_SECRET_ACCESS_KEY"])
}

func TestAnIAMRefusalIsReturnedAndNothingIsWritten(t *testing.T) {
	account, env := &iamAccount{err: errors.New("AccessDenied")}, &environment{}

	err := agent.BackupKey{Users: account, Cluster: env, Spec: keySpec}.Reconcile(t.Context())

	require.Error(t, err)
	assert.Zero(t, env.secretWrites)
}

func TestTheAgentRemovesTheUserWhenTheControlPlaneAsksAndSaysSo(t *testing.T) {
	account := &iamAccount{user: true}
	env := &environment{requested: true, secret: map[string]string{"AWS_ACCESS_KEY_ID": "AKIAOLD", "AWS_SECRET_ACCESS_KEY": "old"}}
	key := agent.BackupKey{Users: account, Cluster: env, Spec: keySpec}

	require.NoError(t, key.Reconcile(t.Context()))
	require.NoError(t, key.Reconcile(t.Context()))

	assert.False(t, account.user)
	assert.True(t, env.removed, "the control plane waits for this")
	assert.Equal(t, 1, account.removed, "a user removed is not removed again")
}

func TestTheAgentNeverMakesAKeyWhileARemovalIsRequested(t *testing.T) {
	account, env := &iamAccount{}, &environment{requested: true}

	require.NoError(t, agent.BackupKey{Users: account, Cluster: env, Spec: keySpec}.Reconcile(t.Context()))

	assert.Zero(t, account.minted)
	assert.Zero(t, env.secretWrites)
}

func TestARemovalIAMRefusedIsNotReportedDone(t *testing.T) {
	account, env := &iamAccount{err: errors.New("AccessDenied")}, &environment{requested: true}

	require.Error(t, agent.BackupKey{Users: account, Cluster: env, Spec: keySpec}.Reconcile(t.Context()))

	assert.False(t, env.removed)
}
