package pmm

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"
)

// RangeQuery is one PromQL range query. Start, End, and Step are what the
// caller wants back; the adapter picks them from the window the UI asked for,
// never from a client-supplied string.
type RangeQuery struct {
	// Query is the PromQL (MetricsQL) expression.
	Query string
	// Start and End bound the window, inclusive.
	Start time.Time
	End   time.Time
	// Step is the interval between returned points.
	Step time.Duration
}

// Sample is one point of a series.
type Sample struct {
	At    time.Time
	Value float64
}

// Series is one labelled time series. Labels are kept so a caller can tell
// members apart when a query does not aggregate them away.
type Series struct {
	Labels  map[string]string
	Samples []Sample
}

// rangeResponse is the Prometheus query_range envelope, which VictoriaMetrics
// serves unchanged: {"status":"success","data":{"resultType":"matrix",...}}.
type rangeResponse struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
	Data      struct {
		ResultType string      `json:"resultType"`
		Result     []rawSeries `json:"result"`
	} `json:"data"`
}

type rawSeries struct {
	Metric map[string]string `json:"metric"`
	Values []rawSample       `json:"values"`
}

// rawSample is the wire form of a point: a two element array whose first entry
// is a Unix timestamp in seconds and whose second is the value as a string.
type rawSample struct {
	At    float64
	Value float64
	// Finite is false for a NaN or an infinity, which is how Prometheus
	// reports a gap or a division by zero.
	Finite bool
}

// UnmarshalJSON decodes `[1787655600, "18.4"]`.
func (s *rawSample) UnmarshalJSON(data []byte) error {
	var pair []json.RawMessage
	if err := json.Unmarshal(data, &pair); err != nil {
		return fmt.Errorf("pmm: sample is not an array: %w", err)
	}
	if len(pair) != 2 {
		return fmt.Errorf("pmm: sample has %d entries, want 2", len(pair))
	}

	if err := json.Unmarshal(pair[0], &s.At); err != nil {
		return fmt.Errorf("pmm: sample timestamp: %w", err)
	}

	var raw string
	if err := json.Unmarshal(pair[1], &raw); err != nil {
		return fmt.Errorf("pmm: sample value: %w", err)
	}

	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fmt.Errorf("pmm: sample value %q: %w", raw, err)
	}

	s.Value = value
	s.Finite = !math.IsNaN(value) && !math.IsInf(value, 0)

	return nil
}

// toSeries converts the wire form, dropping points that cannot be drawn.
func (r rawSeries) toSeries() Series {
	out := Series{Labels: r.Metric, Samples: make([]Sample, 0, len(r.Values))}
	for _, sample := range r.Values {
		if !sample.Finite {
			continue
		}

		seconds, fraction := math.Modf(sample.At)
		out.Samples = append(out.Samples, Sample{
			At:    time.Unix(int64(seconds), int64(fraction*float64(time.Second))).UTC(),
			Value: sample.Value,
		})
	}

	return out
}
