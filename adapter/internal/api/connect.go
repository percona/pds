package api

import (
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/config"
	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/store"
)

//go:embed connect_bootstrap.sh
var connectBootstrap string

// Optional packaged chart the bootstrap script can curl. The committed file is
// a placeholder so go:embed always has one. Image builds replace it with a
// helm package of charts/pds before go build, so a production
// hub can serve the chart this binary was built from. Local `go run` still
// answers 501 unless that file is replaced.
//
//go:embed connect_chart.tgz
var connectChart []byte

type issueJoinRequest struct {
	Name string `json:"name" validate:"required"`
}

type issueJoinResponse struct {
	Environment string     `json:"environment"`
	Token       string     `json:"token"`
	Command     string     `json:"command,omitempty"`
	HubURL      string     `json:"hubUrl,omitempty"`
	Region      string     `json:"region,omitempty"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
}

type enrollmentRegistry struct {
	Server   string `json:"server,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

type enrollmentResponse struct {
	Environment    string              `json:"environment"`
	HubURL         string              `json:"hubUrl,omitempty"`
	GitURL         string              `json:"gitUrl,omitempty"`
	GitUsername    string              `json:"gitUsername,omitempty"`
	GitPassword    string              `json:"gitPassword,omitempty"`
	GitIdentity    string              `json:"gitIdentity,omitempty"`
	GitIdentityPub string              `json:"gitIdentityPub,omitempty"`
	Registry       *enrollmentRegistry `json:"registry,omitempty"`
	AgentImage     string              `json:"agentImage,omitempty"`
	Namespace      string              `json:"namespace,omitempty"`
	AgentToken     string              `json:"agentToken,omitempty"`
}

func (s *Server) publicHubURL(_ *http.Request) string {
	// Only the configured origin is trusted. Request Host can be set by a
	// proxy, and the join command would then send the token to that host.
	return strings.TrimRight(strings.TrimSpace(s.cfg.HubPublicURL), "/")
}

func (s *Server) clusterRegion() string {
	if region := strings.TrimSpace(s.cfg.HubClusterRegion); region != "" {
		return region
	}

	return "us-west-2"
}

// scriptOrigin is the hub URL baked into generated scripts and the join
// command. Only the configured public URL is used, and the scripts expand
// that value in double quotes, so only a real origin is accepted.
func (s *Server) scriptOrigin(r *http.Request) (string, error) {
	origin := strings.TrimRight(s.publicHubURL(r), "/")
	if err := checkEmbeddableURL(origin, false); err != nil {
		return "", Validation("This control plane has no usable public address, so an environment cannot join.")
	}

	return origin, nil
}

func scriptGitURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if err := checkEmbeddableURL(raw, true); err != nil {
		return "", Validation("This control plane has no usable Git address, so an environment cannot join.")
	}

	return raw, nil
}

func scriptRegion(raw string) string {
	raw = strings.TrimSpace(raw)
	if embeddableRegion.MatchString(raw) {
		return raw
	}

	return "us-west-2"
}

func fillConnectPlaceholders(template, hubURL, gitURL, region string) string {
	body := strings.ReplaceAll(template, "__HUB_URL__", hubURL)
	body = strings.ReplaceAll(body, "__GIT_URL__", gitURL)
	body = strings.ReplaceAll(body, "__DEFAULT_REGION__", region)

	return body
}

var (
	embeddableRegion = regexp.MustCompile(`^[a-z]{2}(-[a-z0-9]+)+$`)
	errNotEmbeddable = errors.New("not an embeddable origin")
)

func checkEmbeddableURL(raw string, allowPath bool) error {
	if err := config.CheckEmbeddableURL(raw, allowPath); err != nil {
		return errNotEmbeddable
	}

	return nil
}

func joinCommand(hubURL, token string) string {
	hubURL = strings.TrimRight(strings.TrimSpace(hubURL), "/")
	if hubURL == "" || token == "" {
		return ""
	}

	script := hubURL + "/api/internal/connect/bootstrap.sh"

	return "curl -fsSL " + shellQuote(script) + " | bash -s -- --token " + shellQuote(token)
}

// shellQuote wraps value so it is one shell word. The join command is pasted
// into a terminal, and both the hub URL and the token can carry metacharacters
// from Host or from a configured public URL.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// handleIssueJoinToken mints a per-environment token and the one spoke command.
func (s *Server) handleIssueJoinToken(w http.ResponseWriter, r *http.Request) {
	hubSvc, err := s.hub(r)
	if err != nil {
		Respond(w, r, nil, hubUnavailable(err))

		return
	}
	var req issueJoinRequest
	if err := s.decode(r, &req); err != nil {
		Respond(w, r, nil, err)

		return
	}
	hubURL, err := s.scriptOrigin(r)
	if err != nil {
		Respond(w, r, nil, err)

		return
	}
	token, err := hubSvc.IssueJoin(r.Context(), req.Name)
	if err != nil {
		Respond(w, r, nil, databaseError(err, req.Name))

		return
	}

	out := issueJoinResponse{
		Environment: req.Name,
		Token:       token,
		Command:     joinCommand(hubURL, token),
		HubURL:      hubURL,
		Region:      scriptRegion(s.clusterRegion()),
	}
	if s.cfg.JoinTokenTTL > 0 {
		expires := time.Now().UTC().Add(s.cfg.JoinTokenTTL)
		out.ExpiresAt = &expires
	}
	RespondWithStatus(w, r, http.StatusCreated, out, nil)
}

// handleConnectBootstrap serves the one-command script that creates an EKS
// environment, installs the platform and Flux, and joins this hub.
func (s *Server) handleConnectBootstrap(w http.ResponseWriter, r *http.Request) {
	// A bearer is optional here. The printed curl has no token, and requiring
	// one would break it. When a token is present, stamp its Team so a later
	// use of s.hub(r) would see it.
	r, _ = s.bindAgentIdentity(r)

	hubURL, err := s.scriptOrigin(r)
	if err != nil {
		Respond(w, r, nil, err)

		return
	}
	gitURL, err := scriptGitURL(s.cfg.HubGitURL)
	if err != nil {
		Respond(w, r, nil, err)

		return
	}
	body := fillConnectPlaceholders(connectBootstrap, hubURL, gitURL, scriptRegion(s.clusterRegion()))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

// handleConnectChart serves the packaged Helm chart bootstrap.sh installs.
func (s *Server) handleConnectChart(w http.ResponseWriter, r *http.Request) {
	if len(connectChart) < 100 {
		Respond(w, r, nil, NotImplemented("This hub image has no packaged chart. Set PDS_CHART to a charts/pds checkout."))

		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="pds.tgz"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(connectChart)
}

// handleConnectEnrollment returns clone credentials and an agent token for a
// still-unused join token. Enrollment consumes the invitation immediately.
// preview=1 names the environment without minting or consuming.
func (s *Server) handleConnectEnrollment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r, bindErr := s.bindAgentIdentity(r)
	if bindErr != nil {
		Respond(w, r, nil, bindErr)

		return
	}
	hubSvc, err := s.hub(r)
	if err != nil {
		Respond(w, r, nil, agentHubError(s.cfg, r, err))

		return
	}
	got := bearerToken(r)
	if r.URL.Query().Get("preview") == "1" {
		name, previewErr := hubSvc.PreviewJoin(r.Context(), got)
		if errors.Is(previewErr, hub.ErrJoinToken) {
			Respond(w, r, nil, Unauthorized("That agent token is not valid."))

			return
		}
		if previewErr != nil {
			Respond(w, r, nil, databaseError(previewErr, name))

			return
		}
		out := enrollmentResponse{
			Environment: name,
			HubURL:      s.publicHubURL(r),
			GitURL:      s.cfg.HubGitURL,
			AgentImage:  s.cfg.HubAgentImage,
			Namespace:   s.cfg.EverestNamespace,
		}
		if s.cfg.HubGitHubConfigured() {
			out.GitURL = s.cfg.HubGitHubSSHURL(hubSvc.GitRepo(name))
		}
		Respond(w, r, out, nil)

		return
	}

	name, identity, pub, agent, err := hubSvc.Enroll(r.Context(), got, s.cfg.HubGitHubConfigured())
	if errors.Is(err, hub.ErrJoinToken) {
		Respond(w, r, nil, Unauthorized("That agent token is not valid."))

		return
	}
	if err != nil {
		Respond(w, r, nil, databaseError(err, name))

		return
	}

	out := enrollmentResponse{
		Environment: name,
		HubURL:      s.publicHubURL(r),
		GitURL:      s.cfg.HubGitURL,
		AgentImage:  s.cfg.HubAgentImage,
		Namespace:   s.cfg.EverestNamespace,
		AgentToken:  agent,
	}
	if s.cfg.HubGitHubConfigured() {
		out.GitURL = s.cfg.HubGitHubSSHURL(hubSvc.GitRepo(name))
		out.GitIdentity = identity
		out.GitIdentityPub = pub
		out.Registry = ghcrPullRegistry(s.cfg.HubAgentImage, s.cfg.HubRegistryToken, s.cfg.HubGitHubToken)
	}
	Respond(w, r, out, nil)
}

// ghcrPullRegistry is the pull Secret bootstrap writes on a spoke so kubelet
// can pull a private GHCR agent image. The password is a packages token,
// never the org git token. Other registries are out of scope.
func ghcrPullRegistry(image, token, gitToken string) *enrollmentRegistry {
	image = strings.TrimSpace(image)
	token = strings.TrimSpace(token)
	gitToken = strings.TrimSpace(gitToken)
	if token == "" || token == gitToken || !strings.HasPrefix(image, "ghcr.io/") {
		return nil
	}

	return &enrollmentRegistry{
		Server:   "ghcr.io",
		Username: "x-access-token",
		Password: token,
	}
}

func bearerToken(r *http.Request) string {
	got, _ := auth.BearerToken(r.Header.Get("Authorization"))

	return got
}

// bindAgentIdentity stamps the Team that minted the bearer, when a lookup
// is wired and the hash is unique. A missing lookup is how tests pin one
// WithHub Service. Unknown and colliding hashes do not invent a Team.
func (s *Server) bindAgentIdentity(r *http.Request) (*http.Request, *AppError) {
	if s == nil || s.tokenTeams == nil || r == nil {
		return r, nil
	}
	got := bearerToken(r)
	if got == "" {
		return r, nil
	}
	team, err := s.tokenTeams.TokenTeam(r.Context(), hub.HashToken(got))
	if err != nil {
		if errors.Is(err, store.ErrTokenUnknown) || errors.Is(err, store.ErrTokenCollision) {
			return r, nil
		}

		return r, hubUnavailable(err)
	}
	if team == 0 {
		return r, nil
	}

	return r.WithContext(auth.WithIdentity(r.Context(), auth.Identity{
		Subject: "team:" + strconv.FormatUint(uint64(team), 10),
		Scope:   auth.Scope{Team: auth.Team{ID: team}},
	})), nil
}

// agentHubError is a failed hub() on an agent route. A deployment that serves
// every Organization, with no stamped Team, means the bearer did not name one,
// which is 401 rather than a downstream fault. A named Organization, or a
// stamped Team whose Service failed to build, stays 502.
func agentHubError(cfg config.Config, r *http.Request, err error) *AppError {
	if err == nil {
		return nil
	}
	if cfg.OrganizationName() == "" {
		if _, ok := auth.TeamFromContext(r.Context()); !ok {
			return Unauthorized("That agent token is not valid.")
		}
	}

	return hubUnavailable(err)
}

func (s *Server) authorizeAgent(r *http.Request, environment string) *AppError {
	got := bearerToken(r)
	if got == "" {
		return Unauthorized("An agent token is required.")
	}
	r, bindErr := s.bindAgentIdentity(r)
	if bindErr != nil {
		return bindErr
	}
	// The shared token is a fleet-wide credential, so it only authorizes the
	// environments it was explicitly scoped to. Without that list it authorizes
	// nothing and the per-environment join token below is the only way in.
	// On a named Organization the later hub() call falls back to its Team. On a
	// star pin the shared token still cannot name one.
	want := s.cfg.HubAgentToken
	if want != "" && sharedTokenCovers(s.cfg.HubAgentEnvironments, environment) &&
		subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1 {
		return nil
	}
	hubSvc, err := s.hub(r)
	if err != nil {
		return agentHubError(s.cfg, r, err)
	}
	if hubSvc != nil {
		name, ok := hubSvc.JoinEnvironment(r.Context(), got)
		if ok && name == environment {
			return nil
		}
	}

	return Unauthorized("That agent token is not valid.")
}

// sharedTokenCovers reports whether the shared agent token was scoped to this
// environment. An empty scope is not a wildcard.
func sharedTokenCovers(environments []string, environment string) bool {
	for _, name := range environments {
		if name == environment {
			return true
		}
	}

	return false
}

// handleHeartbeat records observed state from one spoke agent and returns work.
func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	if name == "" {
		Respond(w, r, nil, Validation("An environment name is required."))

		return
	}
	// The inventory and its ConfigMap both filter on this when they read
	// back, so a name that fails here becomes a row that never persists and
	// an entry nothing can evict.
	if !hub.ValidEnvironmentName(name) {
		Respond(w, r, nil, Validation("An environment name must be lowercase letters, numbers, and dashes."))

		return
	}
	r, bindErr := s.bindAgentIdentity(r)
	if bindErr != nil {
		Respond(w, r, nil, bindErr)

		return
	}
	if err := s.authorizeAgent(r, name); err != nil {
		Respond(w, r, nil, err)

		return
	}

	hubSvc, err := s.hub(r)
	if err != nil {
		Respond(w, r, nil, agentHubError(s.cfg, r, err))

		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(maxRequestBody)+1))
	if err != nil {
		Respond(w, r, nil, Validation("The request body could not be read.").WithCause(err))

		return
	}
	if len(body) > maxRequestBody {
		Respond(w, r, nil, Validation("The heartbeat is larger than 1 MiB.").WithStatus(http.StatusRequestEntityTooLarge))

		return
	}

	var beat hub.Heartbeat
	if len(body) > 0 {
		if err := json.Unmarshal(body, &beat); err != nil {
			Respond(w, r, nil, Validation("The request body is not valid JSON.").WithCause(err))

			return
		}
	}
	if beat.Environment == "" {
		beat.Environment = name
	}
	if beat.Environment != name {
		Respond(w, r, nil, Validation("The environment name in the path and body must match."))

		return
	}
	// Secret answers carry credentials. They go to the request waiting on them
	// and are taken off the beat first, so nothing that records or logs a beat
	// ever holds one.
	secretResults := beat.SecretResults
	beat.SecretResults = nil

	prev := hubSvc.LastInstances(name)
	// authorizeAgent already rejected a token that does not cover this environment.
	// The callback below checks again while the heartbeat is accepted.
	if err := s.observeHeartbeat(r.Context(), beat, prev); err != nil {
		Respond(w, r, nil, Downstream("The heartbeat could not be recorded. Try again in a moment.", err))

		return
	}
	work, ok, err := hubSvc.AcceptHeartbeat(beat, func() bool {
		return s.authorizeAgent(r, name) == nil
	})
	if !ok {
		Respond(w, r, nil, Unauthorized("That agent token is not valid."))

		return
	}
	if err != nil {
		Respond(w, r, nil, databaseError(err, name))

		return
	}
	hubSvc.DeliverSecretResults(name, secretResults)
	s.recordSupportEvents(r.Context(), name, hubSvc.TakeSupportEvents(name))
	Respond(w, r, hub.HeartbeatReply{Work: work, Secrets: hubSvc.TakeSecretRequests(name)}, nil)
}
