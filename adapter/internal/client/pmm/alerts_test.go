package pmm_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/pmm"
)

func alertsFrom(t *testing.T, body string) (pmm.AlertList, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	client, err := pmm.New(pmm.Options{BaseURL: srv.URL, Token: testToken})
	require.NoError(t, err)

	return client.Alerts(context.Background())
}

func alertBody(fingerprint, severity string, startsAt time.Time) string {
	return fmt.Sprintf(`{"fingerprint":%q,"labels":{"severity":%q},"startsAt":%q}`,
		fingerprint, severity, startsAt.Format(time.RFC3339))
}

func TestAlertsDecodeEdgeCases(t *testing.T) {
	one := alertBody("a", "warning", testStart)
	tests := []struct {
		name        string
		body        string
		wantItems   int
		wantTotal   int
		unreachable bool
		invalid     bool
	}{
		{name: "empty list", body: `[]`},
		{name: "null is an empty list", body: `null`},
		{name: "null elements are skipped and not counted", body: `[null,` + one + `,null]`, wantItems: 1, wantTotal: 1},
		{name: "only null elements", body: `[null]`},
		{name: "trailing bytes after the array are ignored", body: `[` + one + `] garbage`, wantItems: 1, wantTotal: 1},
		{name: "empty body is a body that stopped coming", body: ``, unreachable: true},
		{name: "body cut after the opening bracket", body: `[`, unreachable: true},
		{name: "body cut at an element boundary", body: `[` + one + `,`, unreachable: true},
		{name: "body cut inside an element", body: `[` + one[:20], unreachable: true},
		{name: "object is not a list", body: `{}`, invalid: true},
		{name: "string is not a list", body: `"x"`, invalid: true},
		{name: "element that is not an alert", body: `[1]`, invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			list, err := alertsFrom(t, tt.body)
			switch {
			case tt.unreachable:
				require.ErrorIs(t, err, pmm.ErrUnreachable)
			case tt.invalid:
				require.Error(t, err)
				assert.NotErrorIs(t, err, pmm.ErrUnreachable)
				assert.Contains(t, err.Error(), "decode alerts")
			default:
				require.NoError(t, err)
				assert.Len(t, list.Items, tt.wantItems)
				assert.Equal(t, tt.wantTotal, list.Total)
			}
		})
	}
}

func TestAlertsKeepTheMostSevereWhenThereAreMoreThanFit(t *testing.T) {
	var parts []string
	// 2500 warnings in arrival order, a critical at position 2300 and an info
	// at the very end: what is kept follows rank, not the response order.
	for i := range 2500 {
		if i == 2300 {
			parts = append(parts, alertBody("crit", "critical", testStart.Add(time.Hour)))
		}
		parts = append(parts, alertBody(fmt.Sprintf("w%04d", i), "warning", testStart.Add(time.Duration(i)*time.Second)))
	}
	parts = append(parts, alertBody("info", "info", testStart))

	list, err := alertsFrom(t, "["+strings.Join(parts, ",")+"]")

	require.NoError(t, err)
	assert.Equal(t, 2502, list.Total, "the total stays whole")
	require.Len(t, list.Items, 2000)
	assert.Equal(t, "crit", list.Items[0].Fingerprint, "the critical survives and leads")
	assert.Equal(t, "w0000", list.Items[1].Fingerprint, "then warnings, longest firing first")
	assert.Equal(t, "w1998", list.Items[1999].Fingerprint)
}
