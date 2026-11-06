package store_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/store"
)

func TestMigrateRemovesTheKeysOfTheRemovedProviders(t *testing.T) {
	db := newInstall(t).open("acme")
	ctx := t.Context()
	require.NoError(t, db.Migrate(ctx))

	var org store.Organization
	require.NoError(t, db.Gorm().WithContext(ctx).First(&org).Error)

	now := time.Now().UTC()
	for _, row := range []store.HubConnection{
		{Name: "pds-openai", Field: "OPENAI_API_KEY"},
		{Name: "pds-gemini", Field: "GEMINI_API_KEY"},
		{Name: "pds-openrouter", Field: "OPENROUTER_API_KEY"},
		// The credential's name was configurable, so the field is what names the provider.
		{Name: "team-keys", Field: "OPENAI_API_KEY"},
		{Name: "pds-cursor", Field: "CURSOR_API_KEY"},
		{Name: "pds-assistant", Field: "ANTHROPIC_API_KEY"},
		{Name: "servicenow", Field: "SN_CLIENT_TOKEN"},
	} {
		row.OrganizationID = org.ID
		row.Sealed = "sealed"
		row.UpdatedAt = now
		require.NoError(t, db.Gorm().WithContext(ctx).Create(&row).Error)
	}

	require.NoError(t, db.Migrate(ctx))

	var left []string
	require.NoError(t, db.Gorm().WithContext(ctx).Model(&store.HubConnection{}).
		Order("field").Pluck("field", &left).Error)
	assert.Equal(t, []string{"ANTHROPIC_API_KEY", "CURSOR_API_KEY", "SN_CLIENT_TOKEN"}, left,
		"only the keys of the kept providers survive")
}
