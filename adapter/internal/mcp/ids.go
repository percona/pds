package mcp

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/percona/pds/adapter/internal/hub"
)

// databaseID is environment/namespace.name. It comes from a model, so it is
// checked before it is placed in a path, and nothing that fails the check can
// reach another route. A one-segment id is not a database id here.
var databaseID = regexp.MustCompile(`^[a-z0-9-]+/[a-z0-9-]+\.[a-z0-9-]+$`)

// databasePath is the path segment of a database id, with each part encoded
// and the slash kept. No request has been made when it fails.
func databasePath(id string) (string, error) {
	if !databaseID.MatchString(id) {
		return "", refuse(msgInvalidDatabaseID)
	}

	parts := strings.Split(id, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}

	return strings.Join(parts, "/"), nil
}

// environmentPath is the path segment of an environment name. No request has
// been made when it fails.
func environmentPath(name string) (string, error) {
	if !hub.ValidEnvironmentName(name) {
		return "", refuse(msgInvalidEnvironmentName)
	}

	return url.PathEscape(name), nil
}
