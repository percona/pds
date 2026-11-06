package hub

import (
	"context"
	"errors"
	"log/slog"
	"sync"
)

// SecretPMMAdminPassword asks a spoke for the admin password of the monitoring
// server it runs.
const SecretPMMAdminPassword = "pmm-admin-password"

var (
	// ErrSecretTimeout means the spoke did not answer before the caller gave up.
	ErrSecretTimeout = errors.New("the environment did not answer in time")
	// ErrSecretUnavailable means the spoke answered that it could not read it.
	ErrSecretUnavailable = errors.New("the environment could not read the secret")
)

// SecretRequest is one read the hub asks a spoke to make on its next beat.
//
// It is not Work. Work is persisted and handed out again until it is
// acknowledged, and a secret must never be written down or retried later: the
// request lives only as long as the browser request waiting on it.
type SecretRequest struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

// SecretResult is a spoke answering one SecretRequest.
//
// Value is a credential. It is read once into the waiting request and then
// dropped, so it has no place in a log line or a stored beat. Printing the
// struct redacts it for that reason.
type SecretResult struct {
	ID    string `json:"id"`
	Value string `json:"value,omitempty"`
	Error string `json:"error,omitempty"`
}

// LogValue keeps the credential out of structured logs.
func (r SecretResult) LogValue() slog.Value {
	return slog.GroupValue(slog.String("id", r.ID), slog.Bool("hasValue", r.Value != ""))
}

// String keeps the credential out of formatted output.
func (r SecretResult) String() string {
	return "secret result " + r.ID
}

type secretWaiter struct {
	kind string
	sent bool
	done chan SecretResult
}

// secretRelay holds the reads browsers are waiting on, per environment. It is in
// memory only, on purpose.
type secretRelay struct {
	mu      sync.Mutex
	waiting map[string]map[string]*secretWaiter
}

func (r *secretRelay) add(environment, id, kind string) *secretWaiter {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.waiting == nil {
		r.waiting = map[string]map[string]*secretWaiter{}
	}
	if r.waiting[environment] == nil {
		r.waiting[environment] = map[string]*secretWaiter{}
	}
	w := &secretWaiter{kind: kind, done: make(chan SecretResult, 1)}
	r.waiting[environment][id] = w

	return w
}

func (r *secretRelay) remove(environment, id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.waiting[environment], id)
	if len(r.waiting[environment]) == 0 {
		delete(r.waiting, environment)
	}
}

// ReadSecret asks the environment's agent for a secret and waits for the answer.
// The value goes to the caller and nowhere else. The wait ends when the agent
// answers or the context does.
func (s *Service) ReadSecret(ctx context.Context, environment, kind string) (string, error) {
	if err := s.requireInventory(environment); err != nil {
		return "", err
	}
	id, err := mintWorkID()
	if err != nil {
		return "", err
	}
	waiter := s.secrets.add(environment, id, kind)
	defer s.secrets.remove(environment, id)

	select {
	case res := <-waiter.done:
		if res.Error != "" {
			// The agent's text names environment internals. It is logged by the
			// caller's cause chain, never shown.
			return "", errors.Join(ErrSecretUnavailable, errors.New(res.Error))
		}

		return res.Value, nil
	case <-ctx.Done():
		return "", errors.Join(ErrSecretTimeout, ctx.Err())
	}
}

// TakeSecretRequests returns the reads an environment has not been asked for yet.
// Each is handed out once.
func (s *Service) TakeSecretRequests(environment string) []SecretRequest {
	s.secrets.mu.Lock()
	defer s.secrets.mu.Unlock()

	var out []SecretRequest
	for id, w := range s.secrets.waiting[environment] {
		if w.sent {
			continue
		}
		w.sent = true
		out = append(out, SecretRequest{ID: id, Kind: w.kind})
	}

	return out
}

// DeliverSecretResults hands each answer to the request waiting on it. An id
// that is not waiting on this environment is dropped, so one environment cannot answer
// for another.
func (s *Service) DeliverSecretResults(environment string, results []SecretResult) {
	s.secrets.mu.Lock()
	defer s.secrets.mu.Unlock()

	for _, res := range results {
		w, ok := s.secrets.waiting[environment][res.ID]
		if !ok {
			continue
		}
		select {
		case w.done <- res:
		default:
		}
	}
}
