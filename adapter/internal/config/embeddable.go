package config

import (
	"errors"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// ErrNotEmbeddable means the value is not a credential-free HTTP(S) origin
// that can be written into a generated script or a Flux Git URL.
var ErrNotEmbeddable = errors.New("not an embeddable origin")

var (
	embeddableHostname = regexp.MustCompile(`(?i)^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	embeddablePath     = regexp.MustCompile(`^/[A-Za-z0-9._/~-]*$`)
)

// CheckEmbeddableURL is the contract the generated Connect scripts apply.
// allowPath is for HUB_GIT_URL (a repo path). HUB_PUBLIC_URL is an origin,
// so it must not carry a path.
func CheckEmbeddableURL(raw string, allowPath bool) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ErrNotEmbeddable
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ErrNotEmbeddable
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ErrNotEmbeddable
	}
	if parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ErrNotEmbeddable
	}
	if !embeddableHost(parsed.Host) {
		return ErrNotEmbeddable
	}
	path := parsed.EscapedPath()
	if path == "" || path == "/" {
		return nil
	}
	if !allowPath || !embeddablePath.MatchString(path) {
		return ErrNotEmbeddable
	}

	return nil
}

func embeddableHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	hostname := host
	if name, port, err := net.SplitHostPort(host); err == nil {
		hostname = name
		if !embeddablePort(port) {
			return false
		}
	} else if strings.Contains(host, ":") {
		return false
	}
	if ip := net.ParseIP(hostname); ip != nil {
		return true
	}

	return embeddableHostname.MatchString(hostname)
}

func embeddablePort(port string) bool {
	n, err := strconv.Atoi(port)

	return err == nil && n >= 1 && n <= 65535
}
