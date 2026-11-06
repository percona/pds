package store

import (
	"fmt"

	"gorm.io/gorm"
)

// AutoMigrate adds a column it does not find and never renames, so without
// this step an upgrade would leave every existing row behind in the old
// cluster column and table. Removing the step ends the window for deployments
// that have not yet reached this revision.
func legacyColumnModels() []any {
	return []any{&HubWork{}, &HubCloneKey{}, &HistoryRow{}, &UsageSample{}, &JoinPhase{}}
}

// renameLegacyNames moves the cluster table and columns to their environment
// names. Each rename runs only when the old name exists and the new one does
// not, so a second start, or a database created after the rename, is left
// alone.
func renameLegacyNames(db *gorm.DB) error {
	migrator := db.Migrator()

	if migrator.HasTable("hub_clusters") && !migrator.HasTable(&HubEnvironment{}) {
		if err := migrator.RenameTable("hub_clusters", &HubEnvironment{}); err != nil {
			return fmt.Errorf("rename hub_clusters: %w", err)
		}
	}

	for _, model := range legacyColumnModels() {
		if !migrator.HasTable(model) {
			continue
		}
		if migrator.HasColumn(model, "cluster") && !migrator.HasColumn(model, "environment") {
			if err := migrator.RenameColumn(model, "cluster", "environment"); err != nil {
				return fmt.Errorf("rename cluster column: %w", err)
			}
		}
	}

	return nil
}
