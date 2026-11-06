package kube_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientfake "k8s.io/client-go/kubernetes/fake"

	"github.com/percona/pds/adapter/internal/client/kube"
	"github.com/percona/pds/adapter/internal/platform"
)

// cs2 builds a clientset from typed events, which newClientset cannot take
// directly because it wants runtime.Object.
func cs2(t *testing.T, events ...*corev1.Event) *clientfake.Clientset {
	t.Helper()

	objects := make([]runtime.Object, 0, len(events))
	for _, e := range events {
		objects = append(objects, e)
	}

	return clientfake.NewClientset(objects...)
}

func clusterEvent(namespace, object, reason string, at time.Time) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: object + "." + reason, Namespace: namespace},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: object},
		Reason:         reason,
		Message:        reason + " happened",
		Type:           corev1.EventTypeNormal,
		LastTimestamp:  metav1.NewTime(at),
		Count:          1,
	}
}

// TestEventsMatchesTheObjectsDerivedFromADatabase is the reason the match is a
// substring: the events that explain a stuck provision are on the pods and
// claims the operator named after the database, not on the database itself.
func TestLongestDatabaseDoesNotShareAPrefix(t *testing.T) {
	names := []string{"orders", "orders-prod"}
	assert.Equal(t, "orders-prod", kube.LongestDatabase("Pod/orders-prod-pxc-0", names))
	assert.Equal(t, "orders", kube.LongestDatabase("Pod/datadir-orders-pxc-0", names))
}

func TestEventsMatchesTheObjectsDerivedFromADatabase(t *testing.T) {
	at := time.Date(2026, time.August, 31, 4, 0, 0, 0, time.UTC)
	cs := newClientset(t,
		clusterEvent("pds", "orders", "Reconciled", at),
		clusterEvent("pds", "orders-pxc-0", "Scheduled", at.Add(time.Minute)),
		clusterEvent("pds", "datadir-orders-pxc-0", "Provisioning", at.Add(2*time.Minute)),
		clusterEvent("pds", "billing-pxc-0", "Scheduled", at.Add(3*time.Minute)),
	)

	got, err := kube.New(cs).Events(context.Background(), "pds", "orders", time.Time{})

	require.NoError(t, err)
	require.Len(t, got, 3, "the billing pod belongs to another database")
	assert.Equal(t, "Pod/orders", got[0].Object)
	assert.Equal(t, "Pod/orders-pxc-0", got[1].Object)
	assert.Equal(t, "Pod/datadir-orders-pxc-0", got[2].Object)
}

func TestEventsAreOldestFirstAndBoundedBySince(t *testing.T) {
	at := time.Date(2026, time.August, 31, 4, 0, 0, 0, time.UTC)
	cs := newClientset(t,
		clusterEvent("pds", "orders-pxc-0", "Old", at.Add(-time.Hour)),
		clusterEvent("pds", "orders-pxc-0", "Newer", at.Add(time.Minute)),
		clusterEvent("pds", "orders-pxc-0", "Newest", at.Add(2*time.Minute)),
	)

	got, err := kube.New(cs).Events(context.Background(), "pds", "orders", at)

	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "Newer", got[0].Reason)
	assert.Equal(t, "Newest", got[1].Reason)
}

// TestEventsFallsBackToTheCreationStamp covers a controller that writes
// through the newer events API, which leaves the legacy timestamps empty.
func TestEventsFallsBackToTheCreationStamp(t *testing.T) {
	at := time.Date(2026, time.August, 31, 4, 0, 0, 0, time.UTC)
	stamped := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "orders-pxc-0.Started",
			Namespace:         "pds",
			CreationTimestamp: metav1.NewTime(at),
		},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "orders-pxc-0"},
		Reason:         "Started",
		Type:           corev1.EventTypeWarning,
	}

	got, err := kube.New(cs2(t, stamped)).
		Events(context.Background(), "pds", "orders", time.Time{})

	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, at, got[0].At)
	assert.Equal(t, platform.EventWarning, got[0].Type)
}

func TestEventsKeepsTheNewestOfANoisyNamespace(t *testing.T) {
	at := time.Date(2026, time.August, 31, 4, 0, 0, 0, time.UTC)
	var objects []*corev1.Event
	// One pod each, because one reason repeating on one pod folds into a
	// single line and would never reach the bound this test is about.
	for i := range kube.MaxEvents + 25 {
		pod := fmt.Sprintf("orders-pxc-%d", i)
		event := clusterEvent("pds", pod, "Pull", at.Add(time.Duration(i)*time.Second))
		event.Name = fmt.Sprintf("%s.Pull.%d", pod, i)
		objects = append(objects, event)
	}

	got, err := kube.New(cs2(t, objects...)).
		Events(context.Background(), "pds", "orders", time.Time{})

	require.NoError(t, err)
	require.Len(t, got, kube.MaxEvents)
	assert.Equal(t, at.Add(time.Duration(kube.MaxEvents+24)*time.Second), got[len(got)-1].At)
}

func TestEventsWithoutClusterAccessIsUnavailable(t *testing.T) {
	_, err := kube.New(nil).Events(context.Background(), "pds", "orders", time.Time{})

	assert.ErrorIs(t, err, platform.ErrUnavailable)
}

func TestEventsWithoutANameReadsNothing(t *testing.T) {
	at := time.Date(2026, time.August, 31, 4, 0, 0, 0, time.UTC)
	cs := newClientset(t, clusterEvent("pds", "orders-pxc-0", "Scheduled", at))

	got, err := kube.New(cs).Events(context.Background(), "pds", "  ", time.Time{})

	require.NoError(t, err)
	assert.Empty(t, got)
}

// probeEvent is the line a pod writes while it is still starting: one Unhealthy
// warning every few seconds, each with a slightly different message, which is
// why Kubernetes cannot fold them into one record itself.
func probeEvent(namespace, object, message string, at time.Time) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: fmt.Sprintf("%s.unhealthy.%d", object, at.UnixNano()), Namespace: namespace},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: object},
		Reason:         "Unhealthy",
		Message:        message,
		Type:           corev1.EventTypeWarning,
		LastTimestamp:  metav1.NewTime(at),
		Count:          1,
	}
}

func TestEventsFoldsARepeatedReasonOnOneObject(t *testing.T) {
	at := time.Date(2026, time.September, 1, 3, 0, 0, 0, time.UTC)
	cs := cs2(t,
		probeEvent("pds", "orders-pxc-0", "Readiness probe failed: ERROR 2003", at),
		probeEvent("pds", "orders-pxc-0", "Readiness probe failed: ERROR 2013", at.Add(time.Second*20)),
		probeEvent("pds", "orders-pxc-0", "Readiness probe failed: command timed out", at.Add(time.Second*40)),
	)

	got, err := kube.New(cs).Events(context.Background(), "pds", "orders", time.Time{})

	require.NoError(t, err)
	require.Len(t, got, 1, "three probe failures on one pod read as one line")
	assert.Equal(t, "Unhealthy", got[0].Reason)
	assert.Equal(t, int32(3), got[0].Count, "the count carries how often it repeated")
	assert.Equal(t, "Readiness probe failed: command timed out", got[0].Message,
		"the newest line is the one worth reading")
	assert.Equal(t, at.Add(time.Second*40).UTC(), got[0].At)
}

func TestEventsKeepsARepeatedReasonPerObject(t *testing.T) {
	at := time.Date(2026, time.September, 1, 3, 0, 0, 0, time.UTC)
	cs := cs2(t,
		probeEvent("pds", "orders-pxc-0", "Readiness probe failed: ERROR 2003", at),
		probeEvent("pds", "orders-haproxy-0", "Readiness probe failed: ERROR 2013", at.Add(time.Second)),
	)

	got, err := kube.New(cs).Events(context.Background(), "pds", "orders", time.Time{})

	require.NoError(t, err)
	require.Len(t, got, 2, "each pod keeps its own line")
}

// TestEventsFoldsRepeatsWithNoCountSet covers events written through the
// newer events API, which leaves Count at 0 instead of 1. Folding two of them
// must still read as two occurrences, not zero.
func TestEventsFoldsRepeatsWithNoCountSet(t *testing.T) {
	at := time.Date(2026, time.September, 1, 3, 0, 0, 0, time.UTC)
	first := probeEvent("pds", "orders-pxc-0", "Readiness probe failed: ERROR 2003", at)
	first.Count = 0
	second := probeEvent("pds", "orders-pxc-0", "Readiness probe failed: ERROR 2013", at.Add(time.Second*20))
	second.Count = 0
	cs := cs2(t, first, second)

	got, err := kube.New(cs).Events(context.Background(), "pds", "orders", time.Time{})

	require.NoError(t, err)
	require.Len(t, got, 1, "two probe failures on one pod read as one line")
	assert.Equal(t, int32(2), got[0].Count, "each unset count still counts as one occurrence")
}

func TestEventsKeepsDistinctReasonsOnOneObject(t *testing.T) {
	at := time.Date(2026, time.September, 1, 3, 0, 0, 0, time.UTC)
	cs := cs2(t,
		probeEvent("pds", "orders-pxc-0", "Readiness probe failed", at),
		clusterEvent("pds", "orders-pxc-0", "Failed", at.Add(time.Second)),
	)

	got, err := kube.New(cs).Events(context.Background(), "pds", "orders", time.Time{})

	require.NoError(t, err)
	require.Len(t, got, 2, "a folded probe warning never hides a different reason")
}
