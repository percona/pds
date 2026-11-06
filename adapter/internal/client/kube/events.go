package kube

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/percona/pds/adapter/internal/platform"
)

// MaxEvents bounds one read. A namespace running several databases can hold
// thousands of events; a timeline reads the recent ones and says so when it
// had to stop.
const MaxEvents = 200

// Events returns the events in a namespace that concern one database, newest
// last, from `since` onward.
//
// The match is a substring rather than involvedObject.name, because the events
// that explain a stuck provision are almost never on the Instance itself. They
// are on the objects the operator derived from it: pods named
// `<instance>-pxc-0`, and volume claims named `datadir-<instance>-pxc-0`,
// where the instance name sits in the middle. Databases live in a namespace
// the platform owns, so a name that appears inside another object's name
// belongs to that database.
func (c *Client) Events(
	ctx context.Context,
	namespace, name string,
	since time.Time,
) ([]platform.ClusterEvent, error) {
	if err := c.available(); err != nil {
		return nil, err
	}

	name = strings.TrimSpace(name)
	if namespace == "" || name == "" {
		return nil, nil
	}

	list, err := c.clientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list events in %q: %w", namespace, err)
	}

	out := make([]platform.ClusterEvent, 0, len(list.Items))
	for _, item := range list.Items {
		if !strings.Contains(item.InvolvedObject.Name, name) {
			continue
		}

		at := eventTime(item.LastTimestamp.Time, item.FirstTimestamp.Time, item.CreationTimestamp.Time)
		if !since.IsZero() && at.Before(since) {
			continue
		}

		out = append(out, platform.ClusterEvent{
			At:      at.UTC(),
			Type:    item.Type,
			Reason:  item.Reason,
			Message: item.Message,
			Object:  item.InvolvedObject.Kind + "/" + item.InvolvedObject.Name,
			Count:   item.Count,
		})
	}

	out = foldRepeats(out)
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })

	// Truncation drops the oldest, so the newest of a noisy window survives.
	if len(out) > MaxEvents {
		out = out[len(out)-MaxEvents:]
	}

	return out, nil
}

// foldRepeats collapses the events that repeat one reason on one object into a
// single line.
//
// A pod that is still starting reports a probe failure every few seconds, and
// each one carries a different message: a refused connection, then a dropped
// handshake, then a timeout. Kubernetes only folds records whose message
// matches, so it cannot fold these, and a create ends up with a dozen lines
// that all say the database is not up yet. That buries the events which
// explain a provision that is genuinely stuck, and it fills the read bound
// before those events are reached.
//
// The newest line survives and carries the total, so a probe that never passes
// still reads as a problem rather than disappearing.
func foldRepeats(in []platform.ClusterEvent) []platform.ClusterEvent {
	type key struct{ object, reason string }

	seen := make(map[key]int, len(in))
	out := make([]platform.ClusterEvent, 0, len(in))

	for _, item := range in {
		k := key{object: item.Object, reason: item.Reason}

		index, ok := seen[k]
		if !ok {
			seen[k] = len(out)
			out = append(out, item)

			continue
		}

		folded := out[index]
		folded.Count = occurrences(folded) + occurrences(item)
		if !item.At.Before(folded.At) {
			folded.At = item.At
			folded.Message = item.Message
			folded.Type = item.Type
		}
		out[index] = folded
	}

	return out
}

// occurrences reads a count Kubernetes left unset as the one event it is.
func occurrences(e platform.ClusterEvent) int32 {
	if e.Count < 1 {
		return 1
	}

	return e.Count
}

// eventTime picks the first timestamp Kubernetes actually filled in. An event
// the API server folded repeats into carries LastTimestamp; one written by a
// controller using the newer events API can carry only a creation time.
func eventTime(candidates ...time.Time) time.Time {
	for _, at := range candidates {
		if !at.IsZero() {
			return at
		}
	}

	return time.Time{}
}

// NamespaceEvents returns recent events in one namespace, newest last.
// Callers that have several databases attribute each event once.
func (c *Client) NamespaceEvents(
	ctx context.Context,
	namespace string,
	since time.Time,
) ([]platform.ClusterEvent, error) {
	if err := c.available(); err != nil {
		return nil, err
	}
	if namespace == "" {
		return nil, nil
	}
	list, err := c.clientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list events in %q: %w", namespace, err)
	}
	out := make([]platform.ClusterEvent, 0, len(list.Items))
	for _, item := range list.Items {
		at := eventTime(item.LastTimestamp.Time, item.FirstTimestamp.Time, item.CreationTimestamp.Time)
		if !since.IsZero() && at.Before(since) {
			continue
		}
		out = append(out, platform.ClusterEvent{
			At:      at.UTC(),
			Type:    item.Type,
			Reason:  item.Reason,
			Message: item.Message,
			Object:  item.InvolvedObject.Kind + "/" + item.InvolvedObject.Name,
			Count:   item.Count,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })

	return out, nil
}

// LongestDatabase is the longest database name contained in an object name.
// orders-prod-pxc-0 belongs to orders-prod, not orders.
func LongestDatabase(object string, names []string) string {
	best := ""
	for _, name := range names {
		if name == "" || !strings.Contains(object, name) {
			continue
		}
		if len(name) > len(best) {
			best = name
		}
	}

	return best
}
