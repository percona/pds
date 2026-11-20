package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTheSTSRegionIsTheConfiguredOneOrUSEast1(t *testing.T) {
	assert.Equal(t, "eu-west-1", defaultRegion("eu-west-1"))
	assert.Equal(t, "us-east-1", defaultRegion(""))
}

func TestTheDefaultInspectorBuildsFromTheEnvironment(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIATEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AWS_REGION", "eu-west-1")

	inspector, err := NewDefaultInspector(t.Context())

	assert.NoError(t, err)
	assert.Equal(t, "eu-west-1", inspector.base.Region)
}
