package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/percona/pds/adapter/internal/service"
)

// metricsResponse is one answer to GET /api/databases/:id/metrics.
//
// Pending means there is nothing to draw yet and nothing to report as broken
// either: PMM is not configured, not up, or has no target for this database.
// PendingReason says which. Every chart still ships, so the UI renders the
// same layout with pending badges instead of a different screen.
type metricsResponse struct {
	DatabaseID    string           `json:"databaseId"`
	Window        string           `json:"window"`
	Start         time.Time        `json:"start"`
	End           time.Time        `json:"end"`
	StepSeconds   int              `json:"stepSeconds"`
	Pending       bool             `json:"pending"`
	PendingReason string           `json:"pendingReason,omitempty"`
	Metrics       []metricResponse `json:"metrics"`
}

// metricResponse is one chart. Current, Min, Max, and Average are the text
// equivalent of the chart, and are zero while the chart is pending or the
// window holds no points, which Pending and Points already say.
type metricResponse struct {
	Kind    string                `json:"kind"`
	Unit    string                `json:"unit"`
	Pending bool                  `json:"pending"`
	Current float64               `json:"current"`
	Min     float64               `json:"min"`
	Max     float64               `json:"max"`
	Average float64               `json:"average"`
	Trend   string                `json:"trend"`
	Points  []metricPointResponse `json:"points"`
}

// metricPointResponse is one reading. The names are short because a 24 hour
// window at a 15 minute step is 96 of them per chart.
type metricPointResponse struct {
	At    time.Time `json:"t"`
	Value float64   `json:"v"`
}

// handleDatabaseMetrics returns the metric series behind the Overview minis and
// the Metrics tab.
func (s *Server) handleDatabaseMetrics(w http.ResponseWriter, r *http.Request) {
	id := databaseID(r)

	window, err := service.ParseMetricWindow(r.URL.Query().Get("window"))
	if err != nil {
		Respond(w, r, nil, metricsError(err))

		return
	}

	kinds, err := service.ParseMetricKinds(r.URL.Query().Get("metrics"))
	if err != nil {
		Respond(w, r, nil, metricsError(err))

		return
	}

	// The metrics of a database the control plane cannot find are a 404, not an
	// empty chart: a wrong id should say so rather than look like a quiet environment.
	// Lookup rather than Get: credentials are not needed to draw a chart, and
	// the list page asks for one CPU series per ready database.
	instance, err := s.databases.Lookup(r.Context(), id)
	if err != nil {
		Respond(w, r, nil, databaseError(err, service.NameFromID(id)))

		return
	}

	request := service.MetricsRequest{
		Database: instance.Name,
		Window:   window,
		Kinds:    kinds,
	}
	if _, err := s.hub(r); err != nil {
		Respond(w, r, nil, hubUnavailable(err))

		return
	}
	snapshot, err := s.environmentMetrics(r, id, request)
	if err != nil {
		Respond(w, r, nil, metricsError(err))

		return
	}

	Respond(w, r, toMetricsResponse(id, *snapshot), nil)
}

// environmentMetrics answers from the readings the environment agent posted with its
// usage samples. This process has no monitoring of its own.
func (s *Server) environmentMetrics(r *http.Request, id string, request service.MetricsRequest) (*service.MetricSnapshot, error) {
	book := s.book()
	if book == nil {
		return s.metrics.FromSamples(nil, request)
	}
	window, err := service.ParseMetricWindow(string(request.Window))
	if err != nil {
		return nil, err
	}
	since := s.metrics.Now().Add(-service.MetricWindowSpan(window))
	points, err := book.ListUsage(r.Context(), service.UsageQuery{
		DatabaseID: id,
		Since:      since,
		// A day of five minute samples is 288 rows, inside one page.
		Limit: service.MaxActivityEvents,
	})
	if err != nil {
		return nil, err
	}

	return s.metrics.FromSamples(points, request)
}

// metricsError maps a metrics failure onto the API error contract. A PMM that
// answers with an error is a downstream failure; a window or chart the
// adapter does not serve is a client that skipped the UI.
func metricsError(err error) *AppError {
	if err == nil {
		return nil
	}

	switch {
	case errors.Is(err, service.ErrUnknownMetricWindow):
		return Validation("Pick a window: 1h or 24h.").WithCause(err)

	case errors.Is(err, service.ErrUnknownMetric):
		return Validation("That metric does not exist. Ask for cpu, memory, connections, replication-lag, or disk.").
			WithCause(err)

	case errors.Is(err, service.ErrInvalidMetricTarget):
		return Validation("That database name cannot be used to look up metrics.").WithCause(err)

	default:
		return Downstream("Metrics are not available right now. Try again in a moment.", err)
	}
}

func toMetricsResponse(id string, in service.MetricSnapshot) metricsResponse {
	out := metricsResponse{
		DatabaseID:    id,
		Window:        string(in.Window),
		Start:         in.Start.UTC(),
		End:           in.End.UTC(),
		StepSeconds:   int(in.Step.Seconds()),
		Pending:       in.Pending,
		PendingReason: in.PendingReason,
		Metrics:       make([]metricResponse, 0, len(in.Series)),
	}

	for _, series := range in.Series {
		out.Metrics = append(out.Metrics, toMetricResponse(series))
	}

	return out
}

func toMetricResponse(in service.MetricSeries) metricResponse {
	out := metricResponse{
		Kind:    string(in.Kind),
		Unit:    in.Unit,
		Pending: in.Pending,
		Current: in.Current,
		Min:     in.Min,
		Max:     in.Max,
		Average: in.Average,
		Trend:   in.Trend,
		Points:  make([]metricPointResponse, 0, len(in.Points)),
	}

	for _, point := range in.Points {
		out.Points = append(out.Points, metricPointResponse{At: point.At.UTC(), Value: point.Value})
	}

	return out
}
