package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/client/aws"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/managed"
	"github.com/percona/pds/adapter/internal/provision"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
	"github.com/percona/pds/adapter/internal/testutil"
)

// testTeamHeader picks the Team a test request signs in as. With none the request
// is Team 7's User.
const testTeamHeader = "X-Test-Team"

// testRoleHeader makes the signed in member an Organization admin when it says so.
const testRoleHeader = "X-Test-Role"

// signedIn gives every request the identity of a User of the Team the header
// names, or of Team 7.
type signedIn struct{}

func (signedIn) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		team := uint(7)
		if raw := r.Header.Get(testTeamHeader); raw != "" {
			n, _ := strconv.Atoi(raw)
			team = uint(n)
		}
		role := auth.RoleUser
		if r.Header.Get(testRoleHeader) == auth.RoleAdmin {
			role = auth.RoleAdmin
		}
		scope := auth.Scope{UserID: 3, OrganizationID: 1, Role: role, Team: auth.Team{ID: team, Name: "team-" + strconv.Itoa(int(team))}}
		next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), auth.Identity{Subject: "u", Scope: scope})))
	})
}

// memoryManaged keeps managed environments and their runs the way the stores
// do, as far as the routes can tell.
type memoryManaged struct {
	rows    map[string]store.ManagedEnvironment
	runs    []store.ProvisioningRun
	steps   map[uint][]store.ProvisioningStep
	unknown map[string]bool
}

func newMemoryManaged() *memoryManaged {
	return &memoryManaged{rows: map[string]store.ManagedEnvironment{}, steps: map[uint][]store.ProvisioningStep{}, unknown: map[string]bool{}}
}

func key(team uint, name string) string { return strconv.Itoa(int(team)) + "/" + name }

func (m *memoryManaged) Create(_ context.Context, in store.NewManagedEnvironment, _ time.Time) (store.ManagedEnvironment, error) {
	if _, dup := m.rows[key(in.TeamID, in.Name)]; dup {
		return store.ManagedEnvironment{}, store.ErrManagedEnvironmentExists
	}
	row := store.ManagedEnvironment{ID: in.ID, TeamID: in.TeamID, Name: in.Name, ConnectionID: in.ConnectionID, Region: in.Region}
	m.rows[key(in.TeamID, in.Name)] = row

	return row, nil
}

func (m *memoryManaged) Remove(_ context.Context, team uint, name string) error {
	delete(m.rows, key(team, name))

	return nil
}

func (m *memoryManaged) List(_ context.Context, team uint) ([]store.ManagedEnvironment, error) {
	var out []store.ManagedEnvironment
	for _, row := range m.rows {
		if row.TeamID == team {
			out = append(out, row)
		}
	}
	slices.SortFunc(out, func(a, b store.ManagedEnvironment) int { return strings.Compare(a.Name, b.Name) })

	return out, nil
}

func (m *memoryManaged) Get(_ context.Context, team uint, name string) (store.ManagedEnvironment, error) {
	row, ok := m.rows[key(team, name)]
	if !ok {
		return store.ManagedEnvironment{}, store.ErrManagedEnvironmentUnknown
	}

	return row, nil
}

func (m *memoryManaged) Start(_ context.Context, team uint, environment, kind string) (store.ProvisioningRun, error) {
	if m.unknown[kind] {
		return store.ProvisioningRun{}, provision.ErrUnknownKind
	}
	if kind != store.KindDelete {
		for _, run := range m.runs {
			if run.TeamID == team && run.Environment == environment && slices.Contains([]string{store.RunQueued, store.RunRunning, store.RunPaused, store.RunFailed}, run.Status) {
				return store.ProvisioningRun{}, store.ErrRunActive
			}
		}
	}
	run := store.ProvisioningRun{ID: uint(len(m.runs) + 1), TeamID: team, Environment: environment, Kind: kind, Status: store.RunQueued}
	m.runs = append(m.runs, run)
	if kind == managed.KindCreate {
		for i, name := range managed.CreateStepNames {
			m.steps[run.ID] = append(m.steps[run.ID], store.ProvisioningStep{RunID: run.ID, Position: i, Name: name, Status: store.StepPending})
		}
	}

	return run, nil
}

func (m *memoryManaged) latest(team uint, environment string) (int, bool) {
	for i := len(m.runs) - 1; i >= 0; i-- {
		if m.runs[i].TeamID == team && m.runs[i].Environment == environment {
			return i, true
		}
	}

	return 0, false
}

func (m *memoryManaged) Retry(_ context.Context, team uint, environment string) error {
	i, ok := m.latest(team, environment)
	if !ok || m.runs[i].Status != store.RunFailed {
		return store.ErrNoFailedRun
	}
	m.runs[i].Status = store.RunQueued

	return nil
}

func (m *memoryManaged) Latest(_ context.Context, team uint, environment string) (store.ProvisioningRun, error) {
	i, ok := m.latest(team, environment)
	if !ok {
		return store.ProvisioningRun{}, store.ErrRunNotFound
	}

	return m.runs[i], nil
}

func (m *memoryManaged) Steps(_ context.Context, runID uint) ([]store.ProvisioningStep, error) {
	return m.steps[runID], nil
}

// memoryRemover deletes through the same run book and forgets in PDS only when
// the connection is Unreachable, as managed.Remover does.
type memoryRemover struct {
	runs    *memoryManaged
	cloudDB *testutil.CloudStore
	removed []string
}

func (r *memoryRemover) Delete(ctx context.Context, scope auth.Scope, name string) (store.ProvisioningRun, error) {
	return r.runs.Start(ctx, scope.Team.ID, name, store.KindDelete)
}

func (r *memoryRemover) RemoveFromPDS(_ context.Context, scope auth.Scope, name string) error {
	if r.cloudDB.Connections[r.runs.rows[key(scope.Team.ID, name)].ConnectionID].State != store.CloudUnreachable {
		return managed.ErrConnectionReachable
	}
	r.removed = append(r.removed, key(scope.Team.ID, name))

	return nil
}

type managedHTTP struct {
	srv     *api.Server
	cloud   *service.CloudConnections
	cloudDB *testutil.CloudStore
	managed *memoryManaged
	remover *memoryRemover
	scope   auth.Scope
	// connection is a Ready connection of Team 7, with the id the tests use.
	connection string
}

// newManagedHTTP builds the router with cloud connections and managed
// environments on in-memory stores, and one connected environment, spoke-a.
func newManagedHTTP(t *testing.T) *managedHTTP {
	t.Helper()

	m := newMemoryManaged()
	cloudDB := testutil.NewCloudStore()
	inspector := &answeringInspector{aws.Inspection{AccountID: "123456789012", StackStatus: "CREATE_COMPLETE", TemplateVersion: service.CloudTemplateVersion}}
	cloud := service.NewCloudConnections(cloudDB, inspector, service.CloudOptions{PrincipalARN: cloudPrincipal, Environments: m})
	scope := auth.Scope{UserID: 3, OrganizationID: 1, Role: auth.RoleUser, Team: auth.Team{ID: 7, Name: "team-7"}}

	issued, err := cloud.IssueToken(t.Context(), scope, []string{"eu-west-1", "us-east-1"})
	require.NoError(t, err)
	enrolled, err := cloud.Enroll(t.Context(), issued.Token, service.CloudAccount{ID: "123456789012", Alias: "acme", Region: "eu-west-1"})
	require.NoError(t, err)
	_, err = cloud.Verify(t.Context(), scope, enrolled.ConnectionID)
	require.NoError(t, err)

	inv := hub.NewInventory(time.Minute)
	inv.Register("spoke-a", "pds")
	hubSvc := hub.NewService(hub.NewMemoryStore(), inv, hub.Options{
		Namespace: testEverestNamespace, BackupStorage: testBackupStorage, Tokens: hub.NewTokenStore(nil, time.Hour),
	})
	databases := service.NewDatabase(engine.NewRegistry(), service.WithHub(hubSvc))

	remover := &memoryRemover{runs: m, cloudDB: cloudDB}
	creator := &managed.Environments{Creator: managed.NewCreator(m, cloud, m, managed.CreatorSettings{
		OutgoingCIDRs: []string{"203.0.113.0/24"}, ChartVersion: "1.2.3", ImageRepository: "ghcr.io/example/pds",
	})}
	rows := newMemoryIdentityStore()
	rows.teams = []store.Team{{ID: 7, OrganizationID: 1, Name: "team-7"}, {ID: 8, OrganizationID: 1, Name: "team-8"}}
	identity := service.NewIdentity(rows, service.WithServedOrganization("acme"))
	srv := api.NewServer(api.Deps{
		Config: testConfig(), Logger: log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
		Auth: signedIn{}, Identity: identity, Databases: databases,
		CloudConnections: cloud, ManagedEnvironments: creator,
		Managed: managed.NewManager(m, m, remover),
	})

	return &managedHTTP{srv: srv, cloud: cloud, cloudDB: cloudDB, managed: m, remover: remover, scope: scope, connection: enrolled.ConnectionID}
}

func (h *managedHTTP) do(t *testing.T, team uint, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()

	return h.as(t, team, auth.RoleUser, method, target, body)
}

func (h *managedHTTP) as(t *testing.T, team uint, role, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	if team != 0 {
		req.Header.Set(testTeamHeader, strconv.Itoa(int(team)))
	}
	req.Header.Set(testRoleHeader, role)
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)

	return rec
}

func (h *managedHTTP) create(t *testing.T, name string) map[string]any {
	t.Helper()

	rec := h.do(t, 0, http.MethodPost, "/api/cloud-connections/"+h.connection+"/environments", `{"name":"`+name+`","region":"eu-west-1"}`)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

	return decodeBody(t, rec)
}

func jsonOf(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	return decodeBody(t, rec)
}

func (h *managedHTTP) failRun(id uint, step int, refusal store.StepRefusal, sentence string) {
	h.managed.runs[id-1].Status = store.RunFailed
	h.managed.steps[id][step].Status = store.StepFailed
	h.managed.steps[id][step].Failure = sentence
	h.managed.steps[id][step].Refusal = refusal
}

func TestATokenIsMintedOnceAndItsCommandNeverReadsBack(t *testing.T) {
	h := newManagedHTTP(t)

	rec := h.do(t, 0, http.MethodPost, "/api/cloud-connection-tokens", `{"allowedRegions":["eu-west-1"]}`)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	got := jsonOf(t, rec)
	command, _ := got["command"].(string)
	assert.Contains(t, command, "https://hub.example/api/internal/cloud-connection/connect.sh")
	assert.Contains(t, command, "ccn_")
	assert.Len(t, got["id"], 16)
	assert.NotEmpty(t, got["expiresAt"])
	assert.Equal(t, []any{"eu-west-1"}, got["allowedRegions"])

	list := h.do(t, 0, http.MethodGet, "/api/cloud-connections", "")
	assert.NotContains(t, list.Body.String(), "ccn_")
}

func TestATokenNeedsAtLeastOneRegion(t *testing.T) {
	h := newManagedHTTP(t)

	rec := h.do(t, 0, http.MethodPost, "/api/cloud-connection-tokens", `{"allowedRegions":["Europe"]}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, api.CodeValidation, jsonOf(t, rec)["code"])
}

func TestWithoutAControlPlanePrincipalTheCloudRoutesAnswerNotImplemented(t *testing.T) {
	srv := api.NewServer(api.Deps{Config: testConfig(), Logger: log.New(io.Discard, log.FormatJSON, slog.LevelDebug), Auth: signedIn{}})

	for _, call := range []struct{ method, path string }{
		{http.MethodPost, "/api/cloud-connection-tokens"}, {http.MethodGet, "/api/cloud-connections"},
	} {
		req := httptest.NewRequest(call.method, call.path, strings.NewReader(`{"allowedRegions":["eu-west-1"]}`))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusNotImplemented, rec.Code, call.path)
	}
}

func TestCancellingATokenStopsTheCommandAndAnotherTeamCannot(t *testing.T) {
	h := newManagedHTTP(t)
	minted := jsonOf(t, h.do(t, 0, http.MethodPost, "/api/cloud-connection-tokens", `{"allowedRegions":["eu-west-1"]}`))
	id, _ := minted["id"].(string)

	assert.Equal(t, http.StatusNotFound, h.do(t, 8, http.MethodDelete, "/api/cloud-connection-tokens/"+id, "").Code)
	assert.Equal(t, http.StatusNoContent, h.do(t, 0, http.MethodDelete, "/api/cloud-connection-tokens/"+id, "").Code)
	assert.Equal(t, http.StatusNotFound, h.do(t, 0, http.MethodDelete, "/api/cloud-connection-tokens/"+id, "").Code, "already ended")
}

func TestAConnectionReadsItsStateRegionsAndEnvironmentsAndNeverItsSecret(t *testing.T) {
	h := newManagedHTTP(t)
	h.create(t, "orders")

	rec := h.do(t, 0, http.MethodGet, "/api/cloud-connections/"+h.connection, "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := jsonOf(t, rec)
	assert.Equal(t, h.connection, got["id"])
	assert.Equal(t, "ready", got["state"])
	assert.Equal(t, "123456789012", got["accountId"])
	assert.Equal(t, "acme", got["alias"])
	assert.Equal(t, []any{"eu-west-1", "us-east-1"}, got["allowedRegions"])
	assert.Equal(t, []any{"orders"}, got["environments"])
	assert.NotContains(t, strings.ToLower(rec.Body.String()), "externalid")
	assert.NotContains(t, rec.Body.String(), h.cloudDB.Connections[h.connection].ExternalID)
}

func TestTheListHoldsTheTeamsConnectionsOnly(t *testing.T) {
	h := newManagedHTTP(t)

	own := jsonOf(t, h.do(t, 0, http.MethodGet, "/api/cloud-connections", ""))
	other := jsonOf(t, h.do(t, 8, http.MethodGet, "/api/cloud-connections", ""))

	assert.Len(t, own["cloudConnections"], 1)
	assert.Empty(t, other["cloudConnections"])
	assert.Equal(t, http.StatusNotFound, h.do(t, 8, http.MethodGet, "/api/cloud-connections/"+h.connection, "").Code)
}

func TestTheTypedAccountPathGivesTheSecretOnceAndLeavesTheConnectionPending(t *testing.T) {
	h := newManagedHTTP(t)

	rec := h.do(t, 0, http.MethodPost, "/api/cloud-connections", `{"accountId":"210987654321","allowedRegions":["eu-west-1"]}`)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	got := jsonOf(t, rec)
	assert.NotEmpty(t, got["externalId"])
	assert.NotEmpty(t, got["stackName"])
	assert.Equal(t, "https://hub.example/api/internal/cloud-connection/template.yaml", got["templateUrl"])
	read := jsonOf(t, h.do(t, 0, http.MethodGet, "/api/cloud-connections/"+got["connectionId"].(string), ""))
	assert.Equal(t, "pending", read["state"])
	assert.NotContains(t, read, "externalId")

	bad := h.do(t, 0, http.MethodPost, "/api/cloud-connections", `{"accountId":"nope","allowedRegions":["eu-west-1"]}`)
	assert.Equal(t, http.StatusBadRequest, bad.Code)
}

func TestRemovingAConnectionNeedsItsNameAndNoEnvironments(t *testing.T) {
	h := newManagedHTTP(t)
	h.create(t, "orders")
	path := "/api/cloud-connections/" + h.connection

	assert.Equal(t, http.StatusBadRequest, h.do(t, 0, http.MethodDelete, path, `{"confirm":"wrong"}`).Code)
	inUse := h.do(t, 0, http.MethodDelete, path, `{"confirm":"acme"}`)
	assert.Equal(t, http.StatusConflict, inUse.Code)
	assert.Contains(t, jsonOf(t, inUse)["message"], "environments")

	require.NoError(t, h.managed.Remove(t.Context(), 7, "orders"))
	assert.Equal(t, http.StatusNoContent, h.do(t, 0, http.MethodDelete, path, `{"confirm":"acme"}`).Code)
	assert.Equal(t, http.StatusNotFound, h.do(t, 0, http.MethodGet, path, "").Code)
}

func TestCreatingAnEnvironmentAnswers202WithItCreatingAndItsRun(t *testing.T) {
	h := newManagedHTTP(t)

	got := h.create(t, "orders")

	env, _ := got["environment"].(map[string]any)
	assert.Equal(t, "orders", env["name"])
	assert.Equal(t, "managed", env["origin"])
	assert.Equal(t, "creating", env["state"])
	assert.Equal(t, h.connection, env["connectionId"])
	prov, _ := env["provisioning"].(map[string]any)
	assert.Equal(t, "in_progress", prov["progress"])
	assert.Len(t, prov["phases"], 4)
	run, _ := got["run"].(map[string]any)
	assert.Equal(t, "create", run["kind"])
	assert.NotZero(t, run["id"])
}

func TestCreatingRefusesADuplicateNameAnUnreadyConnectionAndAForeignOne(t *testing.T) {
	h := newManagedHTTP(t)
	h.create(t, "orders")
	path := "/api/cloud-connections/" + h.connection + "/environments"

	dup := h.do(t, 0, http.MethodPost, path, `{"name":"orders","region":"eu-west-1"}`)
	assert.Equal(t, http.StatusConflict, dup.Code)
	assert.Equal(t, http.StatusBadRequest, h.do(t, 0, http.MethodPost, path, `{"name":"Not A Name","region":"eu-west-1"}`).Code)
	assert.Equal(t, http.StatusBadRequest, h.do(t, 0, http.MethodPost, path, `{"name":"billing","region":"ap-south-1"}`).Code, "a region the connection does not allow")
	assert.Equal(t, http.StatusBadRequest, h.do(t, 0, http.MethodPost, path, `{"name":"billing"}`).Code)
	assert.Equal(t, http.StatusNotFound, h.do(t, 8, http.MethodPost, path, `{"name":"billing","region":"eu-west-1"}`).Code)

}

func TestCreatingOnAConnectionThatIsNotReadyConflicts(t *testing.T) {
	h := newManagedHTTP(t)
	typed := jsonOf(t, h.do(t, 0, http.MethodPost, "/api/cloud-connections", `{"accountId":"210987654321","allowedRegions":["eu-west-1"]}`))

	rec := h.do(t, 0, http.MethodPost, "/api/cloud-connections/"+typed["connectionId"].(string)+"/environments", `{"name":"billing","region":"eu-west-1"}`)

	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Empty(t, h.managed.rows, "nothing was recorded")
}

func TestEnvironmentsListManagedAndConnectedOnesWithOriginAndState(t *testing.T) {
	h := newManagedHTTP(t)
	h.create(t, "orders")

	rec := h.do(t, 0, http.MethodGet, "/api/environments", "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rows := map[string]map[string]any{}
	for _, row := range jsonOf(t, rec)["environments"].([]any) {
		m := row.(map[string]any)
		rows[m["name"].(string)] = m
	}
	require.Contains(t, rows, "orders")
	require.Contains(t, rows, "spoke-a")
	assert.Equal(t, "managed", rows["orders"]["origin"])
	assert.Equal(t, "creating", rows["orders"]["state"])
	assert.Equal(t, "pending", rows["orders"]["status"])
	assert.NotNil(t, rows["orders"]["provisioning"])
	assert.Equal(t, "connected", rows["spoke-a"]["origin"])
	assert.Equal(t, "unreachable", rows["spoke-a"]["status"], "registered, and never heard from")
	assert.Equal(t, "degraded", rows["spoke-a"]["state"])
	assert.NotContains(t, rows["spoke-a"], "provisioning")
}

func TestAFailedRefusalReadsTheSameOnTheEnvironmentAsOnItsRun(t *testing.T) {
	h := newManagedHTTP(t)
	run := h.create(t, "orders")["run"].(map[string]any)
	sentence := "AWS account 123456789012 has reached the quota for Elastic IP addresses per Region in eu-west-1, so the Kubernetes cluster orders cannot be built. Raise the quota in AWS, then retry."
	h.failRun(uint(run["id"].(float64)), 1, store.StepRefusal{Reason: "quota", Service: "Amazon EC2", Item: "Elastic IP addresses per Region", Region: "eu-west-1"}, sentence)

	rec := h.do(t, 0, http.MethodGet, "/api/environments/orders", "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := jsonOf(t, rec)
	assert.Equal(t, "failed", got["state"])
	prov := got["provisioning"].(map[string]any)
	assert.Equal(t, "failed", prov["progress"])
	failure := prov["failure"].(map[string]any)
	assert.Equal(t, sentence, failure["sentence"])
	assert.Equal(t, "retry", failure["action"])
	assert.Equal(t, map[string]any{"reason": "quota", "service": "Amazon EC2", "item": "Elastic IP addresses per Region", "region": "eu-west-1"}, failure["refusal"])
}

func TestAnotherTeamsManagedEnvironmentIsNotFound(t *testing.T) {
	h := newManagedHTTP(t)
	h.create(t, "orders")

	assert.Equal(t, http.StatusNotFound, h.do(t, 8, http.MethodGet, "/api/environments/orders", "").Code)
	assert.Equal(t, http.StatusNotFound, h.do(t, 8, http.MethodPost, "/api/environments/orders/runs", `{"kind":"rebuild"}`).Code)
	assert.Equal(t, http.StatusNotFound, h.do(t, 8, http.MethodDelete, "/api/environments/orders", `{"confirm":"orders"}`).Code)
}

func TestAnAdminOfAnotherTeamActsInTheTeamThatOwnsTheEnvironment(t *testing.T) {
	h := newManagedHTTP(t)
	h.create(t, "orders")
	h.managed.runs[0].Status = store.RunSucceeded

	rec := h.as(t, 8, auth.RoleAdmin, http.MethodPost, "/api/environments/orders/runs", `{"kind":"rebuild"}`)

	assert.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
}

func TestARunStartsOnAManagedEnvironmentAndASecondOneConflicts(t *testing.T) {
	h := newManagedHTTP(t)
	h.create(t, "orders")
	h.managed.runs[0].Status = store.RunSucceeded

	first := h.do(t, 0, http.MethodPost, "/api/environments/orders/runs", `{"kind":"upgrade_pds"}`)
	second := h.do(t, 0, http.MethodPost, "/api/environments/orders/runs", `{"kind":"rebuild"}`)

	require.Equal(t, http.StatusAccepted, first.Code, first.Body.String())
	assert.Equal(t, "upgrade_pds", jsonOf(t, first)["run"].(map[string]any)["kind"])
	assert.Equal(t, http.StatusConflict, second.Code)
	assert.Contains(t, jsonOf(t, second)["message"], "run")
}

func TestARunKindMustBeOneAPersonStarts(t *testing.T) {
	h := newManagedHTTP(t)
	h.create(t, "orders")

	for _, kind := range []string{"create", "delete", "nonsense", ""} {
		rec := h.do(t, 0, http.MethodPost, "/api/environments/orders/runs", `{"kind":"`+kind+`"}`)
		assert.Equal(t, http.StatusBadRequest, rec.Code, kind)
	}
}

func TestAKindTheEngineHasNoStepsForAnswersNotImplemented(t *testing.T) {
	h := newManagedHTTP(t)
	h.create(t, "orders")
	h.managed.runs[0].Status = store.RunSucceeded
	h.managed.unknown[managed.KindRebuild] = true

	rec := h.do(t, 0, http.MethodPost, "/api/environments/orders/runs", `{"kind":"rebuild"}`)

	assert.Equal(t, http.StatusNotImplemented, rec.Code)
}

func TestRetryReRunsTheLatestFailedRunOnly(t *testing.T) {
	h := newManagedHTTP(t)
	run := h.create(t, "orders")["run"].(map[string]any)
	id := uint(run["id"].(float64))
	path := "/api/environments/orders/runs/" + strconv.Itoa(int(id)) + "/retry"

	assert.Equal(t, http.StatusConflict, h.do(t, 0, http.MethodPost, path, "").Code, "nothing failed yet")
	h.failRun(id, 2, store.StepRefusal{}, "A step did not finish.")

	assert.Equal(t, http.StatusNotFound, h.do(t, 0, http.MethodPost, "/api/environments/orders/runs/999/retry", "").Code)
	assert.Equal(t, http.StatusNotFound, h.do(t, 0, http.MethodPost, "/api/environments/orders/runs/nope/retry", "").Code)
	rec := h.do(t, 0, http.MethodPost, path, "")
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	assert.Equal(t, "in_progress", jsonOf(t, rec)["provisioning"].(map[string]any)["progress"])
}

func TestDeletingAManagedEnvironmentNeedsItsTypedNameThenStartsADeleteRun(t *testing.T) {
	h := newManagedHTTP(t)
	h.create(t, "orders")

	for _, body := range []string{"", `{"confirm":""}`, `{"confirm":"order"}`} {
		rec := h.do(t, 0, http.MethodDelete, "/api/environments/orders", body)
		assert.Equal(t, http.StatusBadRequest, rec.Code, body)
	}
	assert.Len(t, h.managed.runs, 1)

	rec := h.do(t, 0, http.MethodDelete, "/api/environments/orders", `{"confirm":"orders"}`)

	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	assert.Equal(t, "delete", jsonOf(t, rec)["run"].(map[string]any)["kind"])
	state := jsonOf(t, h.do(t, 0, http.MethodGet, "/api/environments/orders", ""))["state"]
	assert.Equal(t, "deleting", state)
}

func TestRemoveFromPDSOnlyIsRefusedWhileTheAccountAnswersAndWorksOnceItDoesNot(t *testing.T) {
	h := newManagedHTTP(t)
	h.create(t, "orders")
	body := `{"confirm":"orders","scope":"pds_only"}`

	reachable := h.do(t, 0, http.MethodDelete, "/api/environments/orders", body)
	assert.Equal(t, http.StatusConflict, reachable.Code)
	assert.Empty(t, h.remover.removed)

	h.cloudDB.Connections[h.connection].State = store.CloudUnreachable
	done := h.do(t, 0, http.MethodDelete, "/api/environments/orders", body)

	assert.Equal(t, http.StatusNoContent, done.Code, done.Body.String())
	assert.Equal(t, []string{"7/orders"}, h.remover.removed)
	assert.Equal(t, http.StatusBadRequest, h.do(t, 0, http.MethodDelete, "/api/environments/orders", `{"confirm":"orders","scope":"everything"}`).Code)
}

func TestDeletingAConnectedEnvironmentStillRemovesItWithoutAName(t *testing.T) {
	h := newManagedHTTP(t)

	rec := h.do(t, 0, http.MethodDelete, "/api/environments/spoke-a", "")

	assert.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
}

func TestNoManagedRouteAnswersWithAnAWSRoleAnExternalIDOrAnotherAccount(t *testing.T) {
	h := newManagedHTTP(t)
	h.create(t, "orders")
	secret := h.cloudDB.Connections[h.connection].ExternalID

	for _, path := range []string{"/api/cloud-connections", "/api/cloud-connections/" + h.connection, "/api/environments", "/api/environments/orders"} {
		body := h.do(t, 0, http.MethodGet, path, "").Body.String()
		assert.NotContains(t, body, secret, path)
		assert.NotContains(t, body, "arn:aws", path)
	}
	other := json.RawMessage(h.do(t, 8, http.MethodGet, "/api/cloud-connections", "").Body.Bytes())
	assert.NotContains(t, string(other), "123456789012", "another Team is never told the account")
}
