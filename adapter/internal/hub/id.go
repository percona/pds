package hub

import (
	"fmt"
	"strings"

	"github.com/percona/pds/adapter/internal/engine"
)

// JoinID builds the hub-mode database id: environment/namespace.name.
func JoinID(environment, namespace, name string) string {
	return environment + "/" + namespace + "." + name
}

// SplitID parses a hub database id: environment/namespace.name. A one-segment id
// is invalid.
func SplitID(id string) (environment, namespace, name string, err error) {
	id = strings.TrimSpace(id)
	environmentPart, rest, found := strings.Cut(id, "/")
	if id == "" || !found || environmentPart == "" || rest == "" || strings.Contains(rest, "/") {
		return "", "", "", fmt.Errorf("%w: %q", engine.ErrInvalidID, id)
	}

	namespace, name, found = strings.Cut(rest, ".")
	if !found || namespace == "" || name == "" || strings.Contains(name, ".") {
		return "", "", "", fmt.Errorf("%w: %q must be namespace.name", engine.ErrInvalidID, id)
	}

	return environmentPart, namespace, name, nil
}
