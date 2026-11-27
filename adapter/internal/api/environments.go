package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/managed"
	"github.com/percona/pds/adapter/internal/service"
)

type environmentResponse struct {
	Name      string     `json:"name"`
	Namespace string     `json:"namespace,omitempty"`
	Status    string     `json:"status"`
	LastSeen  *time.Time `json:"lastSeen,omitempty"`
	// Team names the Team the environment belongs to. It is set only on a
	// list across Teams, where the name alone is ambiguous.
	Team string `json:"team,omitempty"`
	// Monitoring says new databases on this environment get monitoring.
	Monitoring bool `json:"monitoring"`
	// SupportAccess is the environment's support access window while one is
	// opening or open, and absent otherwise.
	SupportAccess *hub.SupportSummary `json:"supportAccess,omitempty"`
	// Origin says whether the customer connected the environment or PDS built
	// it, and State how it is as a whole.
	Origin string `json:"origin"`
	State  string `json:"state"`
	// ConnectionID, Region and Provisioning are set on a managed environment.
	ConnectionID string                `json:"connectionId,omitempty"`
	Region       string                `json:"region,omitempty"`
	Provisioning *provisioningResponse `json:"provisioning,omitempty"`
	// Lifecycle is a managed environment's upgrades, drift and loss.
	Lifecycle *lifecycleResponse `json:"lifecycle,omitempty"`
}

// environmentListResponse is what the environments page renders, and nothing more.
// Every signed in client polls this route, so the public address of this
// control plane and the Git org behind it stay off it. The connect guide
// serves those to the one page that needs them.
type environmentListResponse struct {
	Hub          bool                  `json:"hub"`
	Environments []environmentResponse `json:"environments"`
}

type connectGuideResponse struct {
	Hub           bool   `json:"hub"`
	HubURL        string `json:"hubUrl,omitempty"`
	AgentToken    string `json:"agentToken,omitempty"`
	GitURL        string `json:"gitUrl,omitempty"`
	GitHubOrg     string `json:"githubOrg,omitempty"`
	Namespace     string `json:"namespace,omitempty"`
	ClusterRegion string `json:"clusterRegion,omitempty"`
}

func (s *Server) hub(r *http.Request) (*hub.Service, error) {
	if s.databases == nil {
		return nil, nil
	}

	return s.databases.Hub(r.Context())
}

func hubUnavailable(err error) *AppError {
	return Downstream("The control plane is not available. Try again in a moment.", err)
}

// handleListEnvironments returns spokes the hub knows about. hub is true even
// when the list is empty, so the UI can offer Connect an environment on day one.
func (s *Server) handleListEnvironments(w http.ResponseWriter, r *http.Request) {
	// Hub stays true because clients that predate this process read the field
	// to decide whether a database id carries an environment.
	out := environmentListResponse{
		Hub:          true,
		Environments: []environmentResponse{},
	}

	appErr := s.eachTeam(r, func(ctx context.Context, team auth.Team, all bool) *AppError {
		rows, err := s.environmentRows(ctx)
		if err != nil {
			return hubUnavailable(err)
		}
		for _, row := range rows {
			if all {
				row.Team = team.Name
			}
			out.Environments = append(out.Environments, row)
		}

		return nil
	})
	if appErr != nil {
		Respond(w, r, nil, appErr)

		return
	}

	Respond(w, r, out, nil)
}

// environmentRows lists the environments of the Team the context acts in.
func (s *Server) environmentRows(ctx context.Context) ([]environmentResponse, error) {
	if s.databases == nil {
		return []environmentResponse{}, nil
	}
	hubSvc, err := s.databases.Hub(ctx)
	if err != nil {
		return nil, err
	}
	if hubSvc == nil {
		return []environmentResponse{}, nil
	}

	support, err := hubSvc.SupportSummaries(ctx)
	if err != nil {
		return nil, err
	}
	managedByName, err := s.managedEnvironments(ctx)
	if err != nil {
		return nil, err
	}
	items := hubSvc.Environments()
	rows := make([]environmentResponse, 0, len(items)+len(managedByName))
	for _, item := range items {
		row := environmentResponse{
			Name:       item.Name,
			Namespace:  item.Namespace,
			Status:     item.Status,
			Monitoring: item.Monitoring,
			Origin:     originConnected,
			State:      connectedState(item.Status == hub.StatusConnected),
		}
		if !item.LastSeen.IsZero() {
			seen := item.LastSeen.UTC()
			row.LastSeen = &seen
		}
		if summary, ok := support[item.Name]; ok {
			row.SupportAccess = &summary
		}
		if env, ok := managedByName[item.Name]; ok {
			row = withManaged(row, env, item.Status == hub.StatusConnected)
			delete(managedByName, item.Name)
		}
		rows = append(rows, row)
	}
	// A managed environment that has not reported yet is not in the inventory.
	for _, env := range managedByName {
		rows = append(rows, withManaged(environmentResponse{Monitoring: false}, env, false))
	}

	return rows, nil
}

// managedEnvironments is the managed environments of the Team the context acts
// in, by name.
func (s *Server) managedEnvironments(ctx context.Context) (map[string]managed.Managed, error) {
	team, ok := auth.TeamFromContext(ctx)
	if !ok || s.managed == nil {
		return map[string]managed.Managed{}, nil
	}
	envs, err := s.managed.Environments(ctx, team)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]managed.Managed, len(envs))
	for _, env := range envs {
		byName[env.Record.Name] = env
	}

	return byName, nil
}

// handleConnectGuide returns this control plane's public URL and the Git
// remote an environment clones.
func (s *Server) handleConnectGuide(w http.ResponseWriter, r *http.Request) {
	Respond(w, r, connectGuideResponse{
		Hub:           true,
		HubURL:        s.publicHubURL(r),
		GitURL:        s.cfg.HubGitURL,
		GitHubOrg:     s.cfg.HubGitHubOrg,
		Namespace:     s.cfg.EverestNamespace,
		ClusterRegion: s.clusterRegion(),
	}, nil)
}

// handleDisconnectEnvironment removes an environment from the hub. It stops short of
// the environment itself: the databases there keep running, and deleting them is
// a separate decision the operator makes per database. Git desired state
// stays so reconnecting this name cannot prune those databases.
func (s *Server) handleDisconnectEnvironment(w http.ResponseWriter, r *http.Request) {
	if s.deleteManagedEnvironment(w, r, strings.TrimSpace(chi.URLParam(r, "name"))) {
		return
	}
	hubSvc, err := s.hub(r)
	if err != nil {
		Respond(w, r, nil, hubUnavailable(err))

		return
	}
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	supportRemoved, err := hubSvc.Disconnect(r.Context(), name)
	if supportRemoved {
		s.activity.Record(r.Context(), service.ActivityEvent{
			Action:      service.ActionSupportAccessRemove,
			Environment: name,
			Target:      name,
			Outcome:     service.OutcomeSucceeded,
			Detail:      "Access ended because the environment was removed.",
		})
	}
	if err != nil {
		Respond(w, r, nil, databaseError(err, name))

		return
	}

	RespondWithStatus(w, r, http.StatusNoContent, nil, nil)
}

func databaseID(r *http.Request) string {
	id := chi.URLParam(r, "id")
	if extra := chi.URLParam(r, "hubID"); extra != "" {
		return id + "/" + extra
	}

	return id
}
