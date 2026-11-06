package api

import (
	"net"
	"net/http"
	"strings"
)

// clientAddress is the client a rate limit counts. The first address in
// X-Forwarded-For is the one the edge says it saw. Anything that is not an
// address falls back to the connection, so a forged header cannot share a
// bucket with every other request.
func clientAddress(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		first, _, _ := strings.Cut(forwarded, ",")
		first = strings.TrimSpace(first)
		if net.ParseIP(first) != nil {
			return first
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return host
}
