package store

import (
	"gorm.io/gorm"
)

// removedProviderFields are the credential fields of the model providers the
// Chat no longer offers. The credential's name was configurable, so the field
// is what says whose key a row holds.
var removedProviderFields = []string{"OPENAI_API_KEY", "GEMINI_API_KEY", "OPENROUTER_API_KEY"}

// removeProviderKeys deletes every stored key of a removed provider, so no
// customer key outlives its provider. It runs at every start and finds nothing
// once the first one has run. Deleting this file and its call in Migrate ends
// the step.
func removeProviderKeys(db *gorm.DB) error {
	return db.Where("field IN ?", removedProviderFields).Delete(&HubConnection{}).Error
}
