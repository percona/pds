package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClientAddress(t *testing.T) {
	tests := []struct {
		name         string
		forwardedFor string
		want         string
	}{
		{name: "client then proxy", forwardedFor: "203.0.113.7, 172.18.0.3", want: "203.0.113.7"},
		{name: "one hop", forwardedFor: "203.0.113.8", want: "203.0.113.8"},
		{name: "ipv6", forwardedFor: " 2001:db8::1 ,172.18.0.3", want: "2001:db8::1"},
		{name: "not an address falls back", forwardedFor: "unknown, 172.18.0.3", want: "10.0.0.5"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = "10.0.0.5:1234"
			req.Header.Set("X-Forwarded-For", tc.forwardedFor)

			assert.Equal(t, tc.want, clientAddress(req))
		})
	}
}
