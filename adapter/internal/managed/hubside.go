package managed

import (
	"context"
	"errors"
	"fmt"

	"github.com/percona/pds/adapter/internal/hub"
)

// TeamHub is one Team's hub: its tokens, Git tree and inventory. *hub.Service
// is the implementation.
type TeamHub interface {
	EnrollManaged(ctx context.Context, name string) (hub.ManagedEnrollment, error)
	CommitFiles(ctx context.Context, environment string, files map[string][]byte, message string) error
	EnvironmentConnected(environment string) bool
	EnvironmentRelease(environment string) (hub.HeartbeatRelease, bool)
}

// HubSide is the Hub the create steps use: the Team's hub service answers each
// call, so one process serving several Teams never mixes their environments.
type HubSide struct {
	hubs   func(ctx context.Context, team uint) (TeamHub, error)
	gitURL func(repo string) string
}

// NewHubSide builds the Hub. hubs resolves a Team's hub service, and gitURL
// turns the environment's repository name into the address Flux clones.
func NewHubSide(hubs func(ctx context.Context, team uint) (TeamHub, error), gitURL func(repo string) string) *HubSide {
	return &HubSide{hubs: hubs, gitURL: gitURL}
}

// Join enrolls the environment and returns what the agent and Flux are given.
func (h *HubSide) Join(ctx context.Context, team uint, environment string) (Join, error) {
	hubs, err := h.hubs(ctx, team)
	if err != nil {
		return Join{}, err
	}
	enrolled, err := hubs.EnrollManaged(ctx, environment)
	if err != nil {
		return Join{}, err
	}
	url := h.gitURL(enrolled.Repo)
	if url == "" {
		return Join{}, errors.New("this control plane has no Git address to give an environment")
	}

	return Join{
		Environment: environment, AgentToken: enrolled.AgentToken, GitURL: url,
		GitIdentity: enrolled.Identity, GitIdentityPub: enrolled.Pub, Namespace: enrolled.Namespace,
	}, nil
}

// WriteRelease commits the release files to the environment's repository.
func (h *HubSide) WriteRelease(ctx context.Context, team uint, environment string, files map[string][]byte) error {
	hubs, err := h.hubs(ctx, team)
	if err != nil {
		return err
	}

	return hubs.CommitFiles(ctx, environment, files, fmt.Sprintf("write the pds release of %s", environment))
}

// Connected reports whether the environment's agent has sent a heartbeat.
func (h *HubSide) Connected(ctx context.Context, team uint, environment string) (bool, error) {
	hubs, err := h.hubs(ctx, team)
	if err != nil {
		return false, err
	}

	return hubs.EnvironmentConnected(environment), nil
}

// Release reports what the environment's agent last said of its pds release.
func (h *HubSide) Release(ctx context.Context, team uint, environment string) (hub.HeartbeatRelease, bool, error) {
	hubs, err := h.hubs(ctx, team)
	if err != nil {
		return hub.HeartbeatRelease{}, false, err
	}
	release, ok := hubs.EnvironmentRelease(environment)

	return release, ok, nil
}
