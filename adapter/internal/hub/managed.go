package hub

import (
	"context"
	"fmt"
	"strings"
)

// ManagedEnrollment is what a managed environment's agent and Flux are given:
// the agent's credential, the read-only clone key when the Git host has one,
// and where the environment's desired state lives.
type ManagedEnrollment struct {
	AgentToken string
	Identity   string
	Pub        string
	// Namespace is where the environment's databases live.
	Namespace string
	// Repo is the Git repository that holds the environment's tree.
	Repo string
}

// EnrollManaged enrolls an environment the control plane builds itself: it
// registers it, drops every credential an earlier attempt minted, and mints one
// agent credential. A rerun of the step that calls it therefore leaves exactly
// one live credential. The credential is returned once, and only its hash is
// kept.
func (s *Service) EnrollManaged(ctx context.Context, name string) (ManagedEnrollment, error) {
	name = strings.TrimSpace(name)
	if !ValidEnvironmentName(name) {
		return ManagedEnrollment{}, fmt.Errorf("%w: %q", ErrInvalidEnvironment, name)
	}
	if err := s.revokeTokens(ctx, name); err != nil {
		return ManagedEnrollment{}, err
	}
	invitation, err := s.IssueJoin(ctx, name)
	if err != nil {
		return ManagedEnrollment{}, err
	}
	_, identity, pub, agent, err := s.Enroll(ctx, invitation, s.git != nil)
	if err != nil {
		return ManagedEnrollment{}, err
	}

	return ManagedEnrollment{AgentToken: agent, Identity: identity, Pub: pub, Namespace: s.ns, Repo: s.GitRepo(name)}, nil
}

func (s *Service) revokeTokens(ctx context.Context, environment string) error {
	s.life.Lock()
	defer s.life.Unlock()
	unlock := s.spokes.lock(environment)
	defer unlock()

	return wrapPersist(s.tokens.RevokeEnvironment(ctx, environment))
}

// CommitFiles commits files into the environment's tree. Every path must be
// under clusters/<environment>/, so a call for one environment cannot write
// into another's.
func (s *Service) CommitFiles(ctx context.Context, environment string, files map[string][]byte, message string) error {
	prefix := "clusters/" + environment + "/"
	for path := range files {
		if !strings.HasPrefix(path, prefix) {
			return fmt.Errorf("refusing to write %s outside %s", path, prefix)
		}
	}
	s.life.Lock()
	defer s.life.Unlock()
	unlock := s.spokes.lock(environment)
	defer unlock()

	if err := s.store.Write(ctx, files, message); err != nil {
		return fmt.Errorf("%w: %w", ErrGitHost, err)
	}

	return nil
}

// EnvironmentConnected reports whether the environment's agent has sent a
// heartbeat recently.
func (s *Service) EnvironmentConnected(environment string) bool {
	got, ok := s.inv.Get(environment, s.now())

	return ok && got.Status == StatusConnected
}
