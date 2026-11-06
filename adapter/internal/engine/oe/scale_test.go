package oe

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/engine"
)

func TestCheckScaleRejectsTheSameValuesOpenEverestDoes(t *testing.T) {
	t.Parallel()

	cpu := 0
	okCPU := 2
	shrink := 10
	replicas := 2
	okReplicas := 3
	storage := 20
	current := StorageState{Present: true, GB: 20}

	err := CheckScale(engine.ScaleInput{}, current, false)
	require.ErrorIs(t, err, engine.ErrEmptyScale)

	err = CheckScale(engine.ScaleInput{CPUCores: &cpu}, current, false)
	var bounds *engine.BoundsError
	require.ErrorAs(t, err, &bounds)
	assert.Equal(t, FieldCPU, bounds.Field)

	err = CheckScale(engine.ScaleInput{StorageGB: &shrink}, current, false)
	require.ErrorIs(t, err, engine.ErrStorageShrink)

	err = CheckScale(engine.ScaleInput{Replicas: &replicas}, current, false)
	var choice *engine.ChoiceError
	require.ErrorAs(t, err, &choice)
	assert.Equal(t, FieldMembers, choice.Field)

	err = CheckScale(engine.ScaleInput{CPUCores: &okCPU}, current, true)
	var busy *engine.BusyError
	require.ErrorAs(t, err, &busy)
	assert.Equal(t, "backup", busy.Operation)

	require.NoError(t, CheckScale(engine.ScaleInput{
		CPUCores:  &okCPU,
		StorageGB: &storage,
		Replicas:  &okReplicas,
	}, current, false))
}

func TestCheckScaleRejectsStorageWhenTheEngineHasNone(t *testing.T) {
	t.Parallel()

	want := 20
	err := CheckScale(engine.ScaleInput{StorageGB: &want}, StorageState{}, false)
	require.True(t, errors.Is(err, engine.ErrUnparsableQuantity), "got %v", err)
}
