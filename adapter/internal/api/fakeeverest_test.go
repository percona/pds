package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/percona/pds/adapter/internal/client/everest"
)

// fakeEverest is an in-memory stand in for the Everest HTTP API. The adapter
// talks to it through the real client, so the handler tests exercise URL
// building, JSON shapes, and status mapping instead of a Go double that would
// agree with any implementation.
//
// The routes are the ones OpenEverest v2 serves:
//
//	/v1/clusters/{environment}/namespaces/{namespace}/instances
//	/v1/clusters/{environment}/namespaces/{namespace}/instances/{name}
//	/v1/clusters/{environment}/namespaces/{namespace}/instances/{name}/connection
//	/v1/clusters/{environment}/namespaces/{namespace}/instances/{name}/backups
//	/v1/clusters/{environment}/namespaces/{namespace}/backups
type fakeEverest struct {
	server *httptest.Server

	mu        sync.Mutex
	instances map[string]*everest.Instance
	backups   map[string][]everest.Backup
	requests  []string

	// failStatus, when non zero, answers every request with that status.
	failStatus int
	// block, when set, holds each request until the request context ends.
	block bool
	// canceled records that a blocked request saw its context canceled.
	canceled chan struct{}
	// connectionMissing answers the connection route with 404, which is
	// what Everest returns before the provider writes the user Secret.
	connectionMissing bool
	// loadBalancerHosts is the address a load balancer has been given, by
	// instance name. The connection route publishes it in place of the
	// in-cluster host, the way the PXC provider does once one exists.
	loadBalancerHosts map[string]string
}

var instancePath = regexp.MustCompile(
	`^/v1/clusters/([^/]+)/namespaces/([^/]+)/instances(?:/([^/]+))?(?:/(connection|backups))?$`)

func newFakeEverest(t *testing.T) *fakeEverest {
	t.Helper()

	f := &fakeEverest{
		instances: make(map[string]*everest.Instance),
		backups:   make(map[string][]everest.Backup),
		canceled:  make(chan struct{}, 1),
	}

	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)

	return f
}

// client returns a real Everest client pointed at the fake.
func (f *fakeEverest) client(t *testing.T) *everest.Client {
	t.Helper()

	client, err := everest.New(everest.Options{
		BaseURL: f.server.URL,
		Token:   "fake-everest-token",
	})
	if err != nil {
		t.Fatalf("build everest client: %v", err)
	}

	return client
}

func (f *fakeEverest) loadBalancerHost(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.loadBalancerHosts[name]
}

func (f *fakeEverest) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	failStatus, block := f.failStatus, f.block
	f.mu.Unlock()

	if block {
		<-r.Context().Done()
		select {
		case f.canceled <- struct{}{}:
		default:
		}

		return
	}

	if failStatus != 0 {
		f.writeError(w, failStatus, "injected failure")

		return
	}

	switch {
	case strings.HasSuffix(r.URL.Path, "/backups") && r.Method == http.MethodPost &&
		!strings.Contains(r.URL.Path, "/instances/"):
		f.createBackup(w, r)
	default:
		f.serveInstances(w, r)
	}
}

func (f *fakeEverest) serveInstances(w http.ResponseWriter, r *http.Request) {
	match := instancePath.FindStringSubmatch(r.URL.Path)
	if match == nil {
		f.writeError(w, http.StatusNotFound, "no such route: "+r.URL.Path)

		return
	}

	name, sub := match[3], match[4]

	switch {
	case name == "" && r.Method == http.MethodGet:
		f.listInstances(w)
	case name == "" && r.Method == http.MethodPost:
		f.createInstance(w, r)
	case sub == "connection":
		f.getConnection(w, name)
	case sub == "backups":
		f.listBackups(w, name)
	case r.Method == http.MethodGet:
		f.getInstance(w, name)
	case r.Method == http.MethodPut:
		f.updateInstance(w, r, name)
	case r.Method == http.MethodDelete:
		f.deleteInstance(w, name)
	default:
		f.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (f *fakeEverest) listInstances(w http.ResponseWriter) {
	f.mu.Lock()
	defer f.mu.Unlock()

	list := everest.InstanceList{
		Items: make([]everest.Instance, 0, len(f.instances)),
	}
	for _, in := range f.instances {
		list.Items = append(list.Items, *in)
	}

	f.writeJSON(w, http.StatusOK, list)
}

func (f *fakeEverest) createInstance(w http.ResponseWriter, r *http.Request) {
	var in everest.Instance
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		f.writeError(w, http.StatusBadRequest, "malformed instance")

		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if _, exists := f.instances[in.Metadata.Name]; exists {
		f.writeError(w, http.StatusInternalServerError,
			"instance with name '"+in.Metadata.Name+
				"' already exists in namespace '"+testEverestNamespace+"'")

		return
	}

	created := time.Date(2026, time.August, 25, 13, 22, 1, 0, time.UTC)
	in.Metadata.CreationTimestamp = &created
	in.Metadata.Namespace = testEverestNamespace
	in.Status.Phase = everest.PhaseProvisioning
	f.instances[in.Metadata.Name] = &in

	f.writeJSON(w, http.StatusCreated, in)
}

func (f *fakeEverest) getInstance(w http.ResponseWriter, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	in, ok := f.instances[name]
	if !ok {
		f.writeError(w, http.StatusNotFound, "instance "+name+" not found")

		return
	}

	f.writeJSON(w, http.StatusOK, in)
}

func (f *fakeEverest) updateInstance(w http.ResponseWriter, r *http.Request, name string) {
	var in everest.Instance
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		f.writeError(w, http.StatusBadRequest, "malformed instance")

		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.instances[name]; !ok {
		f.writeError(w, http.StatusNotFound, "instance "+name+" not found")

		return
	}
	f.instances[name] = &in

	f.writeJSON(w, http.StatusOK, in)
}

func (f *fakeEverest) deleteInstance(w http.ResponseWriter, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.instances[name]; !ok {
		f.writeError(w, http.StatusNotFound, "instance "+name+" not found")

		return
	}
	delete(f.instances, name)

	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeEverest) getConnection(w http.ResponseWriter, name string) {
	f.mu.Lock()
	missing := f.connectionMissing
	in, found := f.instances[name]
	f.mu.Unlock()

	if !found {
		f.writeError(w, http.StatusNotFound, "instance "+name+" not found")

		return
	}
	if missing {
		f.writeError(w, http.StatusNotFound, "connection is not available yet")

		return
	}

	host := name + "-haproxy." + testEverestNamespace
	if lb := f.loadBalancerHost(name); lb != "" {
		host = lb
	}
	port := everest.Port(3306)
	if in.Spec.ProviderRef.Name == everest.ProviderPostgreSQL {
		host = name + "-pgbouncer." + testEverestNamespace
		port = 5432
	}

	f.writeJSON(w, http.StatusOK, everest.Connection{
		Username: "root",
		Password: "s3cr3t-from-everest",
		Host:     host,
		Port:     port,
	})
}

func (f *fakeEverest) listBackups(w http.ResponseWriter, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.instances[name]; !ok {
		f.writeError(w, http.StatusNotFound, "instance "+name+" not found")

		return
	}

	f.writeJSON(w, http.StatusOK, everest.BackupList{Items: f.backups[name]})
}

func (f *fakeEverest) createBackup(w http.ResponseWriter, r *http.Request) {
	var in everest.Backup
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		f.writeError(w, http.StatusBadRequest, "malformed backup")

		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	environment := in.Spec.InstanceName()
	if _, ok := f.instances[environment]; !ok {
		f.writeError(w, http.StatusNotFound, "instance "+environment+" not found")

		return
	}

	created := time.Date(2026, time.August, 25, 13, 22, 1, 0, time.UTC)
	in.Metadata.CreationTimestamp = &created
	in.Status.State = everest.BackupStatePending
	f.backups[environment] = append(f.backups[environment], in)

	f.writeJSON(w, http.StatusCreated, in)
}

func (f *fakeEverest) writeJSON(w http.ResponseWriter, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		f.writeError(w, http.StatusInternalServerError, "marshal failed")

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (f *fakeEverest) writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"message":` + quote(message) + `}`))
}

func quote(s string) string {
	body, err := json.Marshal(s)
	if err != nil {
		return `""`
	}

	return string(body)
}
