package mcp

import (
	"slices"
	"strings"

	"github.com/percona/pds/adapter/internal/engine/oe"
)

// MongoDBEnabled is the server's half of the engine switch, which the engine
// picker in apps/ui/src/features/databases/engines.ts shares: MongoDB is
// offered to customers when both are on. A registry follows it unless a test
// says otherwise, and so does the guidance the plugin ships.
const MongoDBEnabled = true

// An offered engine is one a person may ask the registry to create.
type offered struct {
	id    string
	label string
}

// offeredEngines is the one list the create tool's engine enum, its prose, the
// prompt argument check and the guidance read. MongoDB is on it only once the
// engine switch is on, which keeps the engine picker and the registry in step.
// A database that exists is served whatever this list says.
func offeredEngines(mongodb bool) []offered {
	engines := []offered{{oe.MySQL.ID, "MySQL"}, {oe.PostgreSQL.ID, "PostgreSQL"}}
	if mongodb {
		engines = append(engines, offered{oe.MongoDB.ID, "MongoDB"})
	}

	return engines
}

func engineIDs(engines []offered) []string {
	ids := make([]string, 0, len(engines))
	for _, engine := range engines {
		ids = append(ids, engine.id)
	}

	return ids
}

// joinWords writes a list as prose: "a", "a or b", "a, b or c".
func joinWords(words []string, conjunction string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}

	return strings.Join(words[:len(words)-1], ", ") + " " + conjunction + " " + words[len(words)-1]
}

// engineLabels is the engines' names as a person writes them.
func engineLabels(engines []offered) []string {
	labels := make([]string, 0, len(engines))
	for _, engine := range engines {
		labels = append(labels, engine.label)
	}

	return labels
}

// OfferedEngines is the engines a registry offers by default, as prose: "MySQL,
// PostgreSQL and MongoDB". The Chat's role prompt names them from here.
func OfferedEngines() string {
	return joinWords(engineLabels(offeredEngines(MongoDBEnabled)), "and")
}

func isOffered(engines []offered, id string) bool {
	return slices.Contains(engineIDs(engines), id)
}
