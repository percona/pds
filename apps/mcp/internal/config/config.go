// Package config reads what the environment says about which control plane pds-mcp talks to,
// and refuses the settings that earlier versions used to sign in.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/percona/pds/apps/mcp/internal/messages"
)

// DefaultControlPlane is the control plane pds-mcp uses when PDS_URL is not set.
const DefaultControlPlane = "https://pds.percona.com"

// Config is the control plane one run of pds-mcp talks to.
type Config struct {
	// Origin is the scheme, host and port of the control plane. It is also its OAuth issuer, and it
	// is the only place a token is ever sent.
	Origin string
	// MCPURL is the control plane's MCP endpoint.
	MCPURL string
}

// removed maps each setting earlier versions read to the sentence that says what replaced it.
var removed = []struct{ name, replacement string }{
	{"PDS_USERNAME", messages.RemovedSignIn},
	{"PDS_PASSWORD", messages.RemovedSignIn},
	{"PDS_OPERATOR_TOKEN", messages.RemovedSignIn},
	{"PDS_ORGANIZATION", messages.RemovedChoice},
	{"PDS_TEAM", messages.RemovedChoice},
	{"PDS_ALLOW_WRITES", messages.RemovedAccess},
	{"PDS_ALLOW_DESTRUCTIVE", messages.RemovedAccess},
}

// message is an error that is a sentence for a person, which ends in a full stop.
type message string

func (m message) Error() string { return string(m) }

// Load reads the settings. getenv is os.Getenv outside tests. A removed setting that is set to
// anything stops the run, and the message names the setting and never its value.
func Load(getenv func(string) string) (Config, error) {
	var problems []string
	for _, r := range removed {
		if strings.TrimSpace(getenv(r.name)) != "" {
			problems = append(problems, r.name+" "+r.replacement)
		}
	}
	if len(problems) > 0 {
		return Config{}, errors.New(strings.Join(problems, "\n"))
	}

	raw := strings.TrimSpace(getenv("PDS_URL"))
	if raw == "" {
		raw = DefaultControlPlane
	}
	origin, err := parseOrigin(raw)
	if err != nil {
		return Config{}, err
	}

	return Config{Origin: origin, MCPURL: origin + "/mcp"}, nil
}

func parseOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return "", message(messages.BadControlPlane)
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") ||
		strings.Trim(u.Path, "/") != "" {
		return "", message(messages.BadControlPlane)
	}

	switch u.Scheme {
	case "https":
	case "http":
		if !loopback(u.Hostname()) {
			return "", message(messages.InsecureControlPlane)
		}
	default:
		return "", message(messages.BadControlPlane)
	}

	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := u.Port(); port != "" {
		host += ":" + port
	}

	return u.Scheme + "://" + host, nil
}

// loopback reports whether a host is this machine. Plain http is allowed only there.
func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)

	return ip != nil && ip.IsLoopback()
}

// LoginFile is where the login command keeps what it was given: a file in the user's config
// directory, which the store creates private to the user.
func LoginFile(getenv func(string) string) (string, error) {
	return inConfigDir(getenv, "credentials.json")
}

// ConnectionsFile is where the data server keeps how to reach each database, beside the login file.
// It holds no password.
func ConnectionsFile(getenv func(string) string) (string, error) {
	return inConfigDir(getenv, "connections.json")
}

func inConfigDir(getenv func(string) string, name string) (string, error) {
	base := getenv("XDG_CONFIG_HOME")
	if base == "" {
		home := getenv("HOME")
		if home == "" {
			return "", fmt.Errorf("%s", messages.NoHomeDirectory)
		}
		base = filepath.Join(home, ".config")
	}

	return filepath.Join(base, "pds-mcp", name), nil
}
