package pmm_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/pmm"
)

const testToken = "glsa_service_account_token"

var (
	testStart = time.Date(2026, 8, 25, 11, 0, 0, 0, time.UTC)
	testEnd   = time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
)

// fixture reads a recorded PMM response body.
func fixture(t *testing.T, name string) []byte {
	t.Helper()

	body, err := os.ReadFile(filepath.Join("fixtures", name))
	require.NoError(t, err)

	return body
}

// capture is what the fake PMM saw. Recording the request separately from the
// response keeps the assertions on path, parameters, and auth honest.
type capture struct {
	Method string
	Path   string
	Query  url.Values
	Auth   string
	Accept string
}

// newFakePMM serves one canned response and records the request.
func newFakePMM(t *testing.T, status int, body []byte) (*pmm.Client, *capture) {
	t.Helper()

	got := &capture{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Method = r.Method
		got.Path = r.URL.Path
		got.Query = r.URL.Query()
		got.Auth = r.Header.Get("Authorization")
		got.Accept = r.Header.Get("Accept")

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	return newClient(t, srv.URL), got
}

func newClient(t *testing.T, baseURL string) *pmm.Client {
	t.Helper()

	client, err := pmm.New(pmm.Options{BaseURL: baseURL, Token: testToken})
	require.NoError(t, err)

	return client
}

func testQuery() pmm.RangeQuery {
	return pmm.RangeQuery{
		Query: `sum(mysql_global_status_threads_connected{cluster="orders-dev"})`,
		Start: testStart,
		End:   testEnd,
		Step:  time.Minute,
	}
}

func TestNewValidatesOptions(t *testing.T) {
	tests := []struct {
		name string
		opts pmm.Options
		want string
	}{
		{name: "blank base URL", opts: pmm.Options{}, want: "base URL must not be blank"},
		{
			name: "no scheme",
			opts: pmm.Options{BaseURL: "pmm.pds.svc"},
			want: "needs an http or https scheme",
		},
		{
			name: "no host",
			opts: pmm.Options{BaseURL: "http://"},
			want: "has no host",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := pmm.New(test.opts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.want)
		})
	}
}

func TestQueryRangeCallsThePrometheusPath(t *testing.T) {
	client, got := newFakePMM(t, http.StatusOK, fixture(t, "query-range-cpu.json"))

	_, err := client.QueryRange(context.Background(), testQuery())
	require.NoError(t, err)

	assert.Equal(t, http.MethodGet, got.Method)
	assert.Equal(t, "/prometheus/api/v1/query_range", got.Path)
	assert.Equal(t, "Bearer "+testToken, got.Auth)
	assert.Equal(t, "application/json", got.Accept)

	assert.Equal(t, testQuery().Query, got.Query.Get("query"))
	assert.Equal(t, "1787655600", got.Query.Get("start"))
	assert.Equal(t, "1787659200", got.Query.Get("end"))
	assert.Equal(t, "60", got.Query.Get("step"))
}

func TestQueryRangeDecodesAMatrix(t *testing.T) {
	client, _ := newFakePMM(t, http.StatusOK, fixture(t, "query-range-cpu.json"))

	series, err := client.QueryRange(context.Background(), testQuery())
	require.NoError(t, err)
	require.Len(t, series, 1)

	samples := series[0].Samples
	require.Len(t, samples, 5)
	assert.Equal(t, testStart, samples[0].At.UTC())
	assert.InDelta(t, 18.4, samples[0].Value, 0.0001)
	assert.InDelta(t, 31.6, samples[4].Value, 0.0001)
	assert.Equal(t, testStart.Add(4*time.Minute), samples[4].At.UTC())
}

func TestQueryRangeKeepsEverySeriesAndItsLabels(t *testing.T) {
	client, _ := newFakePMM(t, http.StatusOK, fixture(t, "query-range-connections.json"))

	series, err := client.QueryRange(context.Background(), testQuery())
	require.NoError(t, err)
	require.Len(t, series, 2)

	assert.Equal(t, "orders-dev-pxc-0", series[0].Labels["service_name"])
	assert.Equal(t, "orders-dev-pxc-1", series[1].Labels["service_name"])
	assert.InDelta(t, 12, series[0].Samples[0].Value, 0.0001)
}

func TestQueryRangeReturnsNoSeriesWhenPMMHasNoTarget(t *testing.T) {
	client, _ := newFakePMM(t, http.StatusOK, fixture(t, "query-range-empty.json"))

	series, err := client.QueryRange(context.Background(), testQuery())
	require.NoError(t, err)
	assert.Empty(t, series)
}

// A gap in a series arrives as NaN, and a division by zero as +Inf. Neither can
// be drawn, so the client drops them instead of handing the UI a hole.
func TestQueryRangeDropsSamplesThatAreNotFinite(t *testing.T) {
	client, _ := newFakePMM(t, http.StatusOK, fixture(t, "query-range-nan.json"))

	series, err := client.QueryRange(context.Background(), testQuery())
	require.NoError(t, err)
	require.Len(t, series, 1)

	values := make([]float64, 0, len(series[0].Samples))
	for _, sample := range series[0].Samples {
		values = append(values, sample.Value)
	}

	assert.Equal(t, []float64{0.5, 1.25}, values)
}

func TestQueryRangeRejectsABlankQuery(t *testing.T) {
	client, _ := newFakePMM(t, http.StatusOK, fixture(t, "query-range-cpu.json"))

	query := testQuery()
	query.Query = "  "

	_, err := client.QueryRange(context.Background(), query)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query must not be blank")
}

func TestQueryRangeRejectsANonPositiveStep(t *testing.T) {
	client, _ := newFakePMM(t, http.StatusOK, fixture(t, "query-range-cpu.json"))

	query := testQuery()
	query.Step = 0

	_, err := client.QueryRange(context.Background(), query)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "step must be positive")
}

func TestQueryRangeReportsAQueryError(t *testing.T) {
	client, _ := newFakePMM(t, http.StatusBadRequest, fixture(t, "error-bad-query.json"))

	_, err := client.QueryRange(context.Background(), testQuery())
	require.Error(t, err)

	var apiErr *pmm.APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, http.StatusBadRequest, apiErr.Status)
	assert.Contains(t, apiErr.Message, "cannot parse query")
}

func TestQueryRangeReportsAnUnauthorizedToken(t *testing.T) {
	client, _ := newFakePMM(t, http.StatusUnauthorized, fixture(t, "error-unauthorized.json"))

	_, err := client.QueryRange(context.Background(), testQuery())
	require.Error(t, err)

	var apiErr *pmm.APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, http.StatusUnauthorized, apiErr.Status)
	assert.True(t, errors.Is(err, pmm.ErrUnauthorized))
}

// VictoriaMetrics can answer 200 with a failed status, so the body is checked
// as well as the code.
func TestQueryRangeReportsAFailedStatusOnA200(t *testing.T) {
	client, _ := newFakePMM(t, http.StatusOK, fixture(t, "error-bad-query.json"))

	_, err := client.QueryRange(context.Background(), testQuery())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot parse query")
}

// The token is a credential, so it must never appear in an error string that
// reaches a log line.
func TestQueryRangeErrorsDoNotCarryTheToken(t *testing.T) {
	client, _ := newFakePMM(t, http.StatusUnauthorized, fixture(t, "error-unauthorized.json"))

	_, err := client.QueryRange(context.Background(), testQuery())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), testToken)
}

// A PMM that is not up refuses the connection, so there is no status to read.
// A reader has to be able to tell that from PMM answering, which is what the
// sentinel is for.
func TestQueryRangeReportsAPMMItNeverReached(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close()

	client, err := pmm.New(pmm.Options{BaseURL: base})
	require.NoError(t, err)

	_, err = client.QueryRange(context.Background(), testQuery())
	require.Error(t, err)
	assert.True(t, errors.Is(err, pmm.ErrUnreachable))

	var apiErr *pmm.APIError
	assert.False(t, errors.As(err, &apiErr), "PMM never answered, so there is no API error")
}

// A gateway status is something in front of PMM answering for it, which is the
// same state as a refused connection one hop further out.
func TestQueryRangeReportsAPMMThatIsNotServingYet(t *testing.T) {
	for _, status := range []int{
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	} {
		client, _ := newFakePMM(t, status, []byte(`{"status":"error","error":"upstream is down"}`))

		_, err := client.QueryRange(context.Background(), testQuery())
		require.Error(t, err)
		assert.True(t, errors.Is(err, pmm.ErrUnavailable), "status %d", status)
		assert.False(t, errors.Is(err, pmm.ErrUnreachable), "status %d: PMM was reached", status)
	}
}

// A 500 is PMM itself failing the query, which is a fault rather than a PMM
// that is not up, so it carries neither sentinel.
func TestQueryRangeReportsAPMMFaultAsItself(t *testing.T) {
	client, _ := newFakePMM(t, http.StatusInternalServerError, []byte(`{"status":"error","error":"storage is broken"}`))

	_, err := client.QueryRange(context.Background(), testQuery())
	require.Error(t, err)
	assert.False(t, errors.Is(err, pmm.ErrUnavailable))
	assert.False(t, errors.Is(err, pmm.ErrUnreachable))
}

func TestQueryRangeHonorsACanceledContext(t *testing.T) {
	client, _ := newFakePMM(t, http.StatusOK, fixture(t, "query-range-cpu.json"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.QueryRange(ctx, testQuery())
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
	assert.False(t, errors.Is(err, pmm.ErrUnreachable), "the caller hung up, PMM was not asked")
}

func TestQueryRangeTreatsDeadlineExceededAsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write(fixture(t, "query-range-cpu.json"))
	}))
	t.Cleanup(srv.Close)

	client, err := pmm.New(pmm.Options{BaseURL: srv.URL})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err = client.QueryRange(ctx, testQuery())
	require.Error(t, err)
	assert.True(t, errors.Is(err, pmm.ErrUnreachable))
	assert.True(t, errors.Is(err, context.DeadlineExceeded))
	assert.False(t, errors.Is(err, context.Canceled))
}

func TestQueryRangeWorksWithoutAToken(t *testing.T) {
	var auth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write(fixture(t, "query-range-cpu.json"))
	}))
	t.Cleanup(srv.Close)

	client, err := pmm.New(pmm.Options{BaseURL: srv.URL})
	require.NoError(t, err)

	_, err = client.QueryRange(context.Background(), testQuery())
	require.NoError(t, err)
	assert.Empty(t, auth)
}

func TestAlertsAsksGrafanaForActiveUnsilencedUninhibitedAlerts(t *testing.T) {
	body := `[{"fingerprint":"f1","labels":{"alertname":"A","severity":"warning"},"annotations":{"summary":"s"},"startsAt":"2026-10-13T09:00:00.000Z","endsAt":"2026-10-13T09:05:00.000Z","status":{"state":"active"}}]`
	client, got := newFakePMM(t, http.StatusOK, []byte(body))

	alerts, err := client.Alerts(context.Background())

	require.NoError(t, err)
	assert.Equal(t, http.MethodGet, got.Method)
	assert.Equal(t, "/graph/api/alertmanager/grafana/api/v2/alerts", got.Path)
	assert.Equal(t, "true", got.Query.Get("active"))
	assert.Equal(t, "false", got.Query.Get("silenced"))
	assert.Equal(t, "false", got.Query.Get("inhibited"))
	assert.Equal(t, "Bearer "+testToken, got.Auth)
	assert.Equal(t, 1, alerts.Total)
	require.Len(t, alerts.Items, 1)
	assert.Equal(t, "f1", alerts.Items[0].Fingerprint)
	assert.Equal(t, "warning", alerts.Items[0].Labels["severity"])
	assert.Equal(t, "s", alerts.Items[0].Annotations["summary"])
	assert.Equal(t, time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC), alerts.Items[0].StartsAt.UTC())
}

func TestAlertsCountsEveryAlertOfAListLargerThanItKeeps(t *testing.T) {
	const fired = 6000
	pad := strings.Repeat("x", 2000)
	var body strings.Builder
	body.WriteString("[")
	for i := range fired {
		if i > 0 {
			body.WriteString(",")
		}
		fmt.Fprintf(&body, `{"fingerprint":"f%d","labels":{"alertname":"A"},"annotations":{"summary":%q},"startsAt":"2026-10-13T09:00:00.000Z"}`, i, pad)
	}
	body.WriteString("]")
	require.Greater(t, body.Len(), 8<<20, "the list is larger than the old body limit")
	client, _ := newFakePMM(t, http.StatusOK, []byte(body.String()))

	alerts, err := client.Alerts(context.Background())

	require.NoError(t, err)
	assert.Equal(t, fired, alerts.Total)
	assert.NotEmpty(t, alerts.Items)
	assert.Less(t, len(alerts.Items), fired)
	assert.Equal(t, "f0", alerts.Items[0].Fingerprint)
}

func TestAlertsReportsABodyThatStallsAfterTheHeadersAsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"fingerprint":"f1",`))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	client := newClient(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := client.Alerts(ctx)

	assert.ErrorIs(t, err, pmm.ErrUnreachable)
}

func TestAlertsMapsAGatewayStatusToUnavailable(t *testing.T) {
	client, _ := newFakePMM(t, http.StatusBadGateway, []byte(`<html>bad gateway</html>`))

	_, err := client.Alerts(context.Background())

	assert.ErrorIs(t, err, pmm.ErrUnavailable)
}

func TestAlertsRefusesABodyThatIsNotAList(t *testing.T) {
	client, _ := newFakePMM(t, http.StatusOK, []byte(`{"status":"error"}`))

	_, err := client.Alerts(context.Background())

	assert.Error(t, err)
}
