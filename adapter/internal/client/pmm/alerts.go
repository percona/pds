package pmm

import (
	"container/heap"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// AlertsPath is Grafana's Alertmanager alert list, served through the same
// nginx as the metrics API.
const AlertsPath = "/graph/api/alertmanager/grafana/api/v2/alerts"

// Alert is one alert Grafana's Alertmanager holds.
type Alert struct {
	Fingerprint string            `json:"fingerprint"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    time.Time         `json:"startsAt"`
}

// Severity ranks of an alert's severity label, most severe first.
const (
	RankCritical = iota
	RankWarning
	RankInfo
)

// SeverityRank maps a severity label to a rank. An unknown or missing label
// is a warning.
func SeverityRank(label string) int {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "critical", "error":
		return RankCritical
	case "info", "notice":
		return RankInfo
	default:
		return RankWarning
	}
}

// AlertBefore orders alerts the way a beat lists them: most severe first,
// then longest firing, then by fingerprint so the order is total.
func AlertBefore(a, b Alert) bool {
	if ra, rb := SeverityRank(a.Labels["severity"]), SeverityRank(b.Labels["severity"]); ra != rb {
		return ra < rb
	}
	if !a.StartsAt.Equal(b.StartsAt) {
		return a.StartsAt.Before(b.StartsAt)
	}

	return a.Fingerprint < b.Fingerprint
}

// AlertList is what the Alertmanager holds. Total counts every alert in the
// response, and Items holds the first maxKeptAlerts of them in AlertBefore
// order: when more fire than fit, the least severe and most recent are the
// ones left out.
type AlertList struct {
	Items []Alert
	Total int
}

// maxKeptAlerts bounds what is held in memory. A rule that fans out over many
// series can fire thousands of alerts, and the agent sends only a few hundred
// of them, so the rest are counted and dropped as they are read, the lowest ranked
// first.
const maxKeptAlerts = 2000

// Alerts lists the alerts that are firing now: active, not silenced and not
// inhibited. A silence set in PMM therefore removes an alert from the list.
func (c *Client) Alerts(ctx context.Context) (AlertList, error) {
	params := url.Values{}
	params.Set("active", "true")
	params.Set("silenced", "false")
	params.Set("inhibited", "false")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL.String()+AlertsPath+"?"+params.Encode(), nil)
	if err != nil {
		return AlertList{}, fmt.Errorf("pmm: build alerts request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return AlertList{}, transportError(AlertsPath, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return AlertList{}, newAPIError(resp, AlertsPath)
	}

	list, err := decodeAlerts(resp.Body)
	if err != nil {
		if isReadFailure(err) {
			return AlertList{}, transportError(AlertsPath, err)
		}

		return AlertList{}, fmt.Errorf("pmm: decode alerts response: %w", err)
	}

	return list, nil
}

// worstFirst is a heap with the lowest ranked alert on top, so it is the one
// replaced when a better alert arrives.
type worstFirst []Alert

func (h worstFirst) Len() int           { return len(h) }
func (h worstFirst) Less(i, j int) bool { return AlertBefore(h[j], h[i]) }
func (h worstFirst) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *worstFirst) Push(x any)        { *h = append(*h, x.(Alert)) }
func (h *worstFirst) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]

	return last
}

// decodeAlerts reads the JSON array one element at a time and keeps the
// maxKeptAlerts best ranked, so a list of any length costs memory for that
// many alerts only. A body that ends early is io.ErrUnexpectedEOF whichever
// element it ends in. A null body is an empty list, a null element is skipped
// and not counted, and bytes after the closing bracket are not read.
func decodeAlerts(r io.Reader) (AlertList, error) {
	dec := json.NewDecoder(r)
	tok, err := dec.Token()
	if err != nil {
		return AlertList{}, truncated(err)
	}
	if tok == nil {
		return AlertList{}, nil
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '[' {
		return AlertList{}, fmt.Errorf("expected a JSON array, got %v", tok)
	}

	var (
		kept  worstFirst
		total int
	)
	for dec.More() {
		var alert *Alert
		if err := dec.Decode(&alert); err != nil {
			return AlertList{}, truncated(err)
		}
		if alert == nil {
			continue
		}
		total++
		switch {
		case len(kept) < maxKeptAlerts:
			heap.Push(&kept, *alert)
		case AlertBefore(*alert, kept[0]):
			kept[0] = *alert
			heap.Fix(&kept, 0)
		}
	}
	if _, err := dec.Token(); err != nil {
		return AlertList{}, truncated(err)
	}

	items := []Alert(kept)
	sort.Slice(items, func(i, j int) bool { return AlertBefore(items[i], items[j]) })

	return AlertList{Items: items, Total: total}, nil
}

// truncated makes a clean end of input mid-array the same error as an end
// inside an element.
func truncated(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}

	return err
}

// isReadFailure tells a body that stopped coming (a timeout, a cancelled
// context, a dropped connection) from a body that arrived and is not an alert
// list. The first is the same state as an unreachable PMM.
func isReadFailure(err error) bool {
	var netErr net.Error

	return errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.As(err, &netErr)
}
