package everest_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/everest"
)

const (
	testNamespace = "pds"
	testToken     = "everest-jwt-token"
	instancePath  = "/v1/clusters/main/namespaces/pds/instances"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()

	body, err := os.ReadFile(filepath.Join("fixtures", name))
	require.NoError(t, err)

	return body
}

type capture struct {
	Method string
	Path   string
	Query  string
	Auth   string
	Accept string
	Body   []byte
}

func newFakeEverest(t *testing.T, status int, body []byte) (*everest.Client, *capture) {
	t.Helper()

	got := &capture{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Method = r.Method
		got.Path = r.URL.Path
		got.Query = r.URL.RawQuery
		got.Auth = r.Header.Get("Authorization")
		got.Accept = r.Header.Get("Accept")

		got.Body, _ = io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	return newClient(t, srv.URL), got
}

func newClient(t *testing.T, baseURL string) *everest.Client {
	t.Helper()

	client, err := everest.New(everest.Options{BaseURL: baseURL, Token: testToken})
	require.NoError(t, err)

	return client
}

func TestNewValidatesOptions(t *testing.T) {
	tests := []struct {
		name string
		opts everest.Options
		want string
	}{
		{name: "blank base URL", opts: everest.Options{}, want: "base URL"},
		{name: "unparsable base URL", opts: everest.Options{BaseURL: "http://%zz"}, want: "base URL"},
		{name: "missing scheme", opts: everest.Options{BaseURL: "everest:8080"}, want: "scheme"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := everest.New(tt.opts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestNewDefaultsTheClusterName(t *testing.T) {
	client, err := everest.New(everest.Options{BaseURL: "http://everest:8080/"})
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestGetInstanceParsesFixture(t *testing.T) {
	client, got := newFakeEverest(t, http.StatusOK, fixture(t, "instance.json"))

	out, err := client.GetInstance(context.Background(), testNamespace, "orders-dev")
	require.NoError(t, err)

	assert.Equal(t, http.MethodGet, got.Method)
	assert.Equal(t, instancePath+"/orders-dev", got.Path)

	assert.Equal(t, "orders-dev", out.Metadata.Name)
	assert.Equal(t, "pds", out.Metadata.Namespace)
	assert.Equal(t, "dev", out.Metadata.Labels["pds.percona.com/profile"])
	require.NotNil(t, out.Metadata.CreationTimestamp)
	assert.Equal(t, 2026, out.Metadata.CreationTimestamp.Year())

	eng := out.EngineComponent()
	assert.Equal(t, int32(1), eng.ReplicaCount())
	assert.Equal(t, "20Gi", eng.StorageSize())
	assert.Equal(t, "1", eng.CPU())
	assert.Equal(t, "2Gi", eng.Memory())
	assert.Equal(t, everest.ComponentTypeHAProxy, out.ProxyComponent().Type)
	assert.Equal(t, everest.PhaseReady, out.Status.Phase)
	assert.Equal(t, "orders-dev-conn", out.Status.ConnectionSecretName())
}

func TestListInstancesParsesFixture(t *testing.T) {
	client, got := newFakeEverest(t, http.StatusOK, fixture(t, "instance-list.json"))

	out, err := client.ListInstances(context.Background(), testNamespace)
	require.NoError(t, err)

	assert.Equal(t, http.MethodGet, got.Method)
	assert.Equal(t, instancePath, got.Path)

	require.Len(t, out, 2)
	assert.Equal(t, "checkout-prod", out[0].Metadata.Name)
	assert.Equal(t, int32(3), out[0].EngineComponent().ReplicaCount())
	assert.Equal(t, everest.PhaseInitializing, out[0].Status.Phase)
	assert.Equal(t, "orders-dev", out[1].Metadata.Name)
	assert.Equal(t, everest.PhaseReady, out[1].Status.Phase)
}

func TestUpdateInstancePutsToTheItemRoute(t *testing.T) {
	client, got := newFakeEverest(t, http.StatusOK, fixture(t, "instance.json"))

	in := &everest.Instance{Metadata: everest.ObjectMeta{Name: "orders-dev"}}

	_, err := client.UpdateInstance(context.Background(), testNamespace, "orders-dev", in)
	require.NoError(t, err)

	assert.Equal(t, http.MethodPut, got.Method)
	assert.Equal(t, instancePath+"/orders-dev", got.Path)
}

func TestUpdateKeepsSpecFieldsTheClientDoesNotModel(t *testing.T) {
	recorded := fixture(t, "instance-unmodeled.json")
	client, got := newFakeEverest(t, http.StatusOK, recorded)

	var stored everest.Instance
	require.NoError(t, json.Unmarshal(recorded, &stored))

	eng := stored.EngineComponent()
	require.NotNil(t, eng.Resources)
	require.NotNil(t, eng.Resources.Limits)
	eng.Resources.Limits.CPU = "2"
	stored.SetEngineComponent(eng)

	_, err := client.UpdateInstance(context.Background(), testNamespace, "orders-dev", &stored)
	require.NoError(t, err)

	var sent struct {
		Spec struct {
			Parameters json.RawMessage `json:"parameters"`
			DataSource json.RawMessage `json:"dataSource"`
			Components map[string]struct {
				Resources struct {
					Limits map[string]string `json:"limits"`
				} `json:"resources"`
				Parameters json.RawMessage `json:"parameters"`
			} `json:"components"`
		} `json:"spec"`
	}
	require.NoError(t, json.Unmarshal(got.Body, &sent))

	assert.JSONEq(t, `{"restoreWaitTimeout":"10m"}`, string(sent.Spec.Parameters))
	assert.Contains(t, string(sent.Spec.DataSource), "orders-dev-20260824091500")
	assert.Equal(t, "2", sent.Spec.Components["engine"].Resources.Limits["cpu"])
	assert.JSONEq(t, `{"configuration":"max_connections=200"}`, string(sent.Spec.Components["engine"].Parameters))
}

func TestUpdateSendsNoStatus(t *testing.T) {
	client, got := newFakeEverest(t, http.StatusOK, fixture(t, "instance.json"))

	in := &everest.Instance{
		Metadata: everest.ObjectMeta{Name: "orders-dev"},
		Status: everest.InstanceStatus{
			Phase:      "Ready",
			Conditions: []everest.Condition{{Type: "Ready", Status: "True", Reason: "AllComponentsReady"}},
		},
	}

	_, err := client.UpdateInstance(context.Background(), testNamespace, "orders-dev", in)
	require.NoError(t, err)

	var sent map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(got.Body, &sent))
	assert.NotContains(t, sent, "status")
}

func TestUnknownSpecFieldsDoNotLeakIntoAFreshInstance(t *testing.T) {
	encoded, err := json.Marshal(everest.InstanceSpec{
		ProviderRef: everest.ObjectRef{Name: everest.ProviderPXC},
		Components: map[string]everest.ComponentSpec{
			everest.ComponentEngine: {Storage: &everest.Storage{Size: "20Gi"}},
		},
	})
	require.NoError(t, err)

	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &fields))
	assert.ElementsMatch(t, []string{"providerRef", "components"}, keysOf(fields))
	assert.NotContains(t, string(encoded), `"Unknown"`)
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}

	return out
}

func TestDeleteInstanceAcceptsNoContent(t *testing.T) {
	client, got := newFakeEverest(t, http.StatusNoContent, nil)

	require.NoError(t, client.DeleteInstance(context.Background(), testNamespace, "orders-dev"))

	assert.Equal(t, http.MethodDelete, got.Method)
	assert.Equal(t, instancePath+"/orders-dev", got.Path)
}

func TestGetInstanceConnectionParsesFixture(t *testing.T) {
	client, got := newFakeEverest(t, http.StatusOK, fixture(t, "connection.json"))

	out, err := client.GetInstanceConnection(context.Background(), testNamespace, "orders-dev")
	require.NoError(t, err)

	assert.Equal(t, instancePath+"/orders-dev/connection", got.Path)
	assert.Equal(t, "root", out.Username)
	assert.NotEmpty(t, out.Password)
	assert.Equal(t, "orders-dev-haproxy.pds", out.Host)
	assert.Equal(t, everest.Port(3306), out.Port)
	assert.Contains(t, out.URI, "mysql://")
}

func TestListInstanceBackupsParsesFixture(t *testing.T) {
	client, got := newFakeEverest(t, http.StatusOK, fixture(t, "backup-list.json"))

	out, err := client.ListInstanceBackups(context.Background(), testNamespace, "orders-dev")
	require.NoError(t, err)

	assert.Equal(t, instancePath+"/orders-dev/backups", got.Path)

	require.Len(t, out, 2)
	assert.Equal(t, "orders-dev-20260824091500", out[0].Metadata.Name)
	assert.Equal(t, everest.BackupStateSucceeded, out[0].Status.State)
	assert.Equal(t, "orders-dev", out[0].Spec.InstanceName())
	require.NotNil(t, out[0].Status.Size)
	assert.Equal(t, "1289748", *out[0].Status.Size)
	require.NotNil(t, out[0].Status.StartedAt)
	assert.Equal(t, 2026, out[0].Status.StartedAt.Year())
	assert.Equal(t, everest.BackupStateRunning, out[1].Status.State)
}

func TestListInstanceBackupsHandlesAnEmptyCollection(t *testing.T) {
	client, _ := newFakeEverest(t, http.StatusOK, fixture(t, "backup-list-empty.json"))

	out, err := client.ListInstanceBackups(context.Background(), testNamespace, "orders-dev")
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestCreateBackupPostsToTheNamespaceCollection(t *testing.T) {
	client, got := newFakeEverest(t, http.StatusCreated, fixture(t, "backup-list.json"))

	in := &everest.Backup{
		Metadata: everest.ObjectMeta{Name: "orders-dev-20260825132201", Namespace: testNamespace},
		Spec: everest.BackupSpec{
			Origin:     everest.InstanceBackupOrigin("orders-dev"),
			ClassRef:   everest.ObjectRef{Name: everest.BackupClassPXC},
			StorageRef: everest.ObjectRef{Name: "minio"},
		},
	}

	_, err := client.CreateBackup(context.Background(), testNamespace, in)
	require.NoError(t, err)

	assert.Equal(t, http.MethodPost, got.Method)
	assert.Equal(t, "/v1/clusters/main/namespaces/pds/backups", got.Path)

	var sent everest.Backup
	require.NoError(t, json.Unmarshal(got.Body, &sent))
	assert.Equal(t, "Backup", sent.Kind)
	assert.Equal(t, "orders-dev", sent.Spec.InstanceName())
	assert.Equal(t, "minio", sent.Spec.StorageRef.Name)
}

func TestCreateBackupOmitsEmptyStatus(t *testing.T) {
	client, got := newFakeEverest(t, http.StatusCreated, fixture(t, "backup-list.json"))

	in := &everest.Backup{
		Metadata: everest.ObjectMeta{Name: "orders-dev-20260825132201", Namespace: testNamespace},
		Spec: everest.BackupSpec{
			Origin:     everest.InstanceBackupOrigin("orders-dev"),
			ClassRef:   everest.ObjectRef{Name: everest.BackupClassPXC},
			StorageRef: everest.ObjectRef{Name: "minio"},
		},
	}

	_, err := client.CreateBackup(context.Background(), testNamespace, in)
	require.NoError(t, err)

	var sent map[string]any
	require.NoError(t, json.Unmarshal(got.Body, &sent))
	_, hasStatus := sent["status"]
	assert.False(t, hasStatus, "create body must omit status, got %s", got.Body)
}

func TestNotFoundIsErrNotFound(t *testing.T) {
	client, _ := newFakeEverest(t, http.StatusNotFound, fixture(t, "error-not-found.json"))

	_, err := client.GetInstance(context.Background(), testNamespace, "nope-missing")
	require.Error(t, err)
	assert.ErrorIs(t, err, everest.ErrNotFound)

	var apiErr *everest.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotFound, apiErr.Status)
	assert.Contains(t, apiErr.Message, "not found")
}

func TestDuplicateUpdateIsErrConflict(t *testing.T) {
	client, _ := newFakeEverest(t, http.StatusInternalServerError, fixture(t, "error-already-exists.json"))

	in := &everest.Instance{Metadata: everest.ObjectMeta{Name: "checkout-prod"}}

	_, err := client.UpdateInstance(context.Background(), testNamespace, "checkout-prod", in)
	require.Error(t, err)
	assert.ErrorIs(t, err, everest.ErrConflict)
}

func TestConflictStatusIsErrConflict(t *testing.T) {
	client, _ := newFakeEverest(t, http.StatusConflict, []byte(`{"message":"object already exists"}`))

	in := &everest.Instance{Metadata: everest.ObjectMeta{Name: "orders-dev"}}

	_, err := client.UpdateInstance(context.Background(), testNamespace, "orders-dev", in)
	require.Error(t, err)
	assert.ErrorIs(t, err, everest.ErrConflict)
}

func TestWebhookDenialIsNotAConflict(t *testing.T) {
	client, _ := newFakeEverest(t,
		http.StatusUnprocessableEntity, fixture(t, "error-backup-storage-missing.json"))

	in := &everest.Backup{
		Spec: everest.BackupSpec{
			Origin:     everest.InstanceBackupOrigin("orders-dev"),
			StorageRef: everest.ObjectRef{Name: "minio"},
		},
	}

	_, err := client.CreateBackup(context.Background(), testNamespace, in)
	require.Error(t, err)
	assert.NotErrorIs(t, err, everest.ErrConflict)
	assert.NotErrorIs(t, err, everest.ErrNotFound)

	var apiErr *everest.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusUnprocessableEntity, apiErr.Status)
	assert.Contains(t, apiErr.Message, "BackupStorage")
}

func TestServerErrorCarriesStatusAndMessage(t *testing.T) {
	client, _ := newFakeEverest(t,
		http.StatusInternalServerError, []byte(`{"message":"etcdserver: request timed out"}`))

	_, err := client.ListInstances(context.Background(), testNamespace)
	require.Error(t, err)
	assert.NotErrorIs(t, err, everest.ErrNotFound)
	assert.NotErrorIs(t, err, everest.ErrConflict)

	var apiErr *everest.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusInternalServerError, apiErr.Status)
	assert.Contains(t, apiErr.Message, "etcdserver")
}

func TestUnparsableErrorBodyStillReportsStatus(t *testing.T) {
	client, _ := newFakeEverest(t, http.StatusBadGateway, []byte("<html>gateway</html>"))

	_, err := client.ListInstances(context.Background(), testNamespace)
	require.Error(t, err)

	var apiErr *everest.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusBadGateway, apiErr.Status)
	assert.Contains(t, apiErr.Error(), "502")
}

func TestUnparsableSuccessBodyIsAnError(t *testing.T) {
	client, _ := newFakeEverest(t, http.StatusOK, []byte("not json"))

	_, err := client.GetInstance(context.Background(), testNamespace, "orders-dev")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode")
}

func TestEverestDownIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	client := newClient(t, srv.URL)
	srv.Close()

	_, err := client.ListInstances(context.Background(), testNamespace)
	require.Error(t, err)

	var apiErr *everest.APIError
	assert.NotErrorAs(t, err, &apiErr, "a transport failure is not an API error")
}

func TestContextCancellationReachesEverest(t *testing.T) {
	started := make(chan struct{})
	observed := make(chan error, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		observed <- r.Context().Err()
	}))
	t.Cleanup(srv.Close)

	client := newClient(t, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		_, err := client.ListInstances(ctx, testNamespace)
		errCh <- err
	}()

	<-started
	cancel()

	select {
	case err := <-errCh:
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("client did not return after the context was canceled")
	}

	select {
	case err := <-observed:
		assert.True(t, errors.Is(err, context.Canceled), "Everest saw the cancellation, got %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Everest never observed the cancellation")
	}
}

func TestRequestsWithoutATokenOmitTheAuthHeader(t *testing.T) {
	got := &capture{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture(t, "instance-list.json"))
	}))
	t.Cleanup(srv.Close)

	client, err := everest.New(everest.Options{BaseURL: srv.URL})
	require.NoError(t, err)

	_, err = client.ListInstances(context.Background(), testNamespace)
	require.NoError(t, err)
	assert.Empty(t, got.Auth)
}

func TestBlankNamespaceOrNameIsRejectedBeforeTheRequest(t *testing.T) {
	client, got := newFakeEverest(t, http.StatusOK, fixture(t, "instance.json"))

	_, err := client.GetInstance(context.Background(), "", "orders-dev")
	require.Error(t, err)

	_, err = client.GetInstance(context.Background(), testNamespace, "")
	require.Error(t, err)

	assert.Empty(t, got.Method, "no request reached Everest")
}
