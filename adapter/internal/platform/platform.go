// Package platform describes Kubernetes facts the adapter reads for timelines
// and heartbeats. Types here stay free of client-go so the service layer never
// imports a Kubernetes package.
package platform

import (
	"errors"
	"time"
)

// ErrUnavailable means the adapter has no Kubernetes access, which is a
// deployment state rather than a request failure. Local runs without a
// kubeconfig hit this on every read.
var ErrUnavailable = errors.New("cluster access is not available")

// ClusterEvent is one Kubernetes event, reduced to what a timeline shows.
// Count is above one when the API server folded a repeat into the same
// record, which is how "back-off pulling image" reports thirty attempts as
// one row.
type ClusterEvent struct {
	At      time.Time
	Type    string
	Reason  string
	Message string
	Object  string
	Count   int32
}

// Event types Kubernetes emits.
const (
	EventNormal  = "Normal"
	EventWarning = "Warning"
)
