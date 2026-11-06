package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"strings"

	"github.com/percona/pds/adapter/internal/engine"
)

// SampleEvent is one Kubernetes event for a database.
type SampleEvent struct {
	DatabaseID string
	At         time.Time
	Type       string
	Reason     string
	Message    string
	Count      int32
}

// SampleUsage is one resource sample. Nil processor fields mean metrics-server
// did not answer.
type SampleUsage struct {
	DatabaseID    string
	At            time.Time
	CPUAvg        *float64
	CPUMax        *float64
	MemoryAvg     *float64
	MemoryMax     *float64
	StorageBytes  *int64
	MetricsReason string
}

const metricsMiss = "metrics-server did not answer"

type podMetricsList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Containers []struct {
			Usage map[string]string `json:"usage"`
		} `json:"containers"`
	} `json:"items"`
}

// SampleDatabases reads events and one usage sample per database. CPU and
// memory come from metrics-server. When that API does not answer, the sample
// still carries the provisioned storage and says why the processor figures
// are missing.
func (c *Client) SampleDatabases(
	ctx context.Context,
	namespace string,
	items []engine.Instance,
) ([]SampleEvent, []SampleUsage, error) {
	if err := c.available(); err != nil {
		return nil, nil, err
	}
	metrics, metricsErr := c.podMetrics(ctx, namespace)
	events := c.databaseEvents(ctx, namespace, items)
	now := time.Now().UTC()
	usage := make([]SampleUsage, 0, len(items))
	for _, item := range items {
		if item.ID == "" {
			continue
		}
		point := SampleUsage{DatabaseID: item.ID, At: now}
		if item.Sizing.StorageGB > 0 {
			bytes := int64(item.Sizing.StorageGB * 1024 * 1024 * 1024)
			point.StorageBytes = &bytes
		}
		if metricsErr != nil {
			point.MetricsReason = metricsMiss
		} else if sample, ok := metricFor(item.Name, instanceNames(items), metrics); ok {
			cpu, memory := sample.cpu, sample.memory
			point.CPUAvg = &cpu
			point.CPUMax = &cpu
			point.MemoryAvg = &memory
			point.MemoryMax = &memory
		} else {
			point.MetricsReason = metricsMiss
		}
		usage = append(usage, point)
	}

	return events, usage, nil
}

type cpuMem struct {
	cpu    float64
	memory float64
}

func (c *Client) podMetrics(ctx context.Context, namespace string) (out map[string]cpuMem, err error) {
	defer func() {
		if recover() != nil {
			out = nil
			err = fmt.Errorf("%s", metricsMiss)
		}
	}()
	raw, err := c.clientset.CoreV1().RESTClient().Get().
		AbsPath("/apis/metrics.k8s.io/v1beta1/namespaces/" + namespace + "/pods").
		Do(ctx).Raw()
	if err != nil {
		return nil, err
	}
	var list podMetricsList
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	out = map[string]cpuMem{}
	for _, item := range list.Items {
		var sample cpuMem
		for _, container := range item.Containers {
			sample.cpu += parseCPU(container.Usage["cpu"])
			sample.memory += parseMemory(container.Usage["memory"])
		}
		out[item.Metadata.Name] = sample
	}

	return out, nil
}

func (c *Client) databaseEvents(ctx context.Context, namespace string, items []engine.Instance) []SampleEvent {
	since := time.Now().Add(-10 * time.Minute)
	grouped := map[string][]engine.Instance{}
	for _, item := range items {
		ns := item.Namespace
		if ns == "" {
			ns = namespace
		}
		if ns == "" || item.Name == "" {
			continue
		}
		grouped[ns] = append(grouped[ns], item)
	}
	var out []SampleEvent
	for ns, group := range grouped {
		found, err := c.NamespaceEvents(ctx, ns, since)
		if err != nil {
			continue
		}
		names := instanceNames(group)
		for _, event := range found {
			owner := LongestDatabase(event.Object, names)
			if owner == "" {
				continue
			}
			for _, item := range group {
				if item.Name != owner || item.ID == "" {
					continue
				}
				out = append(out, SampleEvent{
					DatabaseID: item.ID,
					At:         event.At,
					Type:       event.Type,
					Reason:     event.Reason,
					Message:    event.Message,
					Count:      event.Count,
				})
				break
			}
		}
	}

	return out
}

func metricFor(name string, names []string, metrics map[string]cpuMem) (cpuMem, bool) {
	if name == "" {
		return cpuMem{}, false
	}
	if sample, ok := metrics[name]; ok {
		return sample, true
	}
	var sum cpuMem
	found := false
	for pod, sample := range metrics {
		if podOwner(pod, names) != name {
			continue
		}
		sum.cpu += sample.cpu
		sum.memory += sample.memory
		found = true
	}

	return sum, found
}

// podOwner is the longest database name that names this pod. A pod is
// named after the database plus a suffix, so orders must not claim
// orders-prod-pxc-0.
func podOwner(pod string, names []string) string {
	best := ""
	for _, name := range names {
		if name == "" {
			continue
		}
		if pod != name && !strings.HasPrefix(pod, name+"-") {
			continue
		}
		if len(name) > len(best) {
			best = name
		}
	}

	return best
}

func instanceNames(items []engine.Instance) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		if item.Name != "" {
			names = append(names, item.Name)
		}
	}

	return names
}

func parseCPU(raw string) float64 {
	q, err := resourceQuantity(raw)
	if err != nil {
		return 0
	}

	return q
}

func parseMemory(raw string) float64 {
	q, err := memoryQuantity(raw)
	if err != nil {
		return 0
	}

	return q
}

func resourceQuantity(raw string) (float64, error) {
	if raw == "" {
		return 0, fmt.Errorf("empty")
	}
	switch {
	case strings.HasSuffix(raw, "n"):
		return scaleSuffix(raw, 1e9)
	case strings.HasSuffix(raw, "u"):
		return scaleSuffix(raw, 1e6)
	case strings.HasSuffix(raw, "m"):
		return scaleSuffix(raw, 1e3)
	}
	var n float64
	if _, err := fmt.Sscan(raw, &n); err != nil {
		return 0, err
	}

	return n, nil
}

func scaleSuffix(raw string, divisor float64) (float64, error) {
	var n float64
	if _, err := fmt.Sscan(raw[:len(raw)-1], &n); err != nil {
		return 0, err
	}

	return n / divisor, nil
}

func parseMilli(raw string) (float64, error) {
	if len(raw) > 1 && raw[len(raw)-1] == 'm' {
		var n float64
		if _, err := fmt.Sscan(raw[:len(raw)-1], &n); err != nil {
			return 0, err
		}

		return n / 1000, nil
	}

	return 0, fmt.Errorf("not a cpu quantity")
}

func memoryQuantity(raw string) (float64, error) {
	if raw == "" {
		return 0, fmt.Errorf("empty")
	}
	mult := 1.0
	suffix := raw
	switch {
	case len(raw) > 2 && raw[len(raw)-2:] == "Ki":
		suffix = raw[:len(raw)-2]
		mult = 1024
	case len(raw) > 2 && raw[len(raw)-2:] == "Mi":
		suffix = raw[:len(raw)-2]
		mult = 1024 * 1024
	case len(raw) > 2 && raw[len(raw)-2:] == "Gi":
		suffix = raw[:len(raw)-2]
		mult = 1024 * 1024 * 1024
	}
	var n float64
	if _, err := fmt.Sscan(suffix, &n); err != nil {
		return 0, err
	}

	return n * mult, nil
}
